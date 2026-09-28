package agent

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"time"

	"remotedesk.local/remotedesk/internal/apiclient"
	"remotedesk.local/remotedesk/internal/identity"
	"remotedesk.local/remotedesk/internal/media"
	"remotedesk.local/remotedesk/internal/protocol"
	"remotedesk.local/remotedesk/internal/stun"
)

func mediaRelayAddr(cfg identity.Config) *net.UDPAddr {
	a := cfg.MediaRelayUDP
	if a == "" {
		a = cfg.STUN
	}
	addr, _ := net.ResolveUDPAddr("udp4", a)
	return addr
}
func nativeOptions(dir string, cfg identity.Config, o media.NativeOptions) media.NativeOptions {
	if o.Executable == "" {
		o.Executable = cfg.MediaExecutable
	}
	if o.Executable != "" && !filepath.IsAbs(o.Executable) {
		o.Executable = filepath.Join(dir, o.Executable)
	}
	return o
}
func runSession(ctx context.Context, dir string, id *identity.Identity, pub []byte, b *media.Broker, o media.NativeOptions, api *apiclient.Client) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if e := b.Run(ctx); e != nil {
			slog.Warn("media broker ended", "error", e)
			cancel()
		}
	}()
	defer func() { cancel(); b.Close(); wg.Wait() }()
	// Revocation is enforced for ongoing media, not only subsequent handshakes.
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				key := protocol.PublicString(pub)
				if !identity.Allowed(dir, key, "desktop") || (o.Host && o.Control && !identity.Allowed(dir, key, "control")) || (o.Host && o.Clipboard && !identity.Allowed(dir, key, "clipboard")) || (o.Host && o.Audio && !identity.Allowed(dir, key, "audio")) {
					cancel()
					return
				}
				checkCtx, done := context.WithTimeout(ctx, 3*time.Second)
				status, err := api.Status(checkCtx, b.SessionID())
				done()
				// A control-plane outage does not immediately kill a working direct
				// session, but positive closure/expiry or revoked identity does.
				if err == nil && (status.Closed || status.ExpiresAt < time.Now().Unix()) {
					cancel()
					return
				}
				var he *apiclient.HTTPError
				if errors.As(err, &he) && (he.Status == 401 || he.Status == 403 || he.Status == 404) {
					cancel()
					return
				}
			}
		}
	}()
	return media.RunNative(ctx, id, pub, b, o)
}
func Desktop(ctx context.Context, dir, target string, relayOnly bool, o media.NativeOptions) error {
	id, cfg, e := identity.Load(dir)
	if e != nil {
		return e
	}
	api, e := apiclient.New(id, cfg)
	if e != nil {
		return e
	}
	defer api.Close()
	p, e := api.Peer(ctx, target)
	if e != nil {
		return e
	}
	pub, e := protocol.ParsePublic(p.Device.PublicKey)
	if e != nil || p.Device.ID != target || protocol.DeviceID(pub) != target || !p.Candidates.Verify(pub) || !p.Online {
		return errors.New("invalid/stale/offline target")
	}
	if !identity.Allowed(dir, p.Device.PublicKey, "desktop") {
		return errors.New("peer is not explicitly authorized for desktop")
	}
	u, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if e != nil {
		return e
	}
	defer u.Close()
	candidates := localCandidates(u.LocalAddr())
	// STUN uses the same socket later used for media, preserving its NAT mapping.
	if a := mediaRelayAddr(cfg); a != nil {
		req, tx := stun.Request()
		_, _ = u.WriteToUDP(req, a)
		_ = u.SetReadDeadline(time.Now().Add(600 * time.Millisecond))
		buf := make([]byte, 1500)
		n, src, e := u.ReadFromUDP(buf)
		if e == nil && src.IP.Equal(a.IP) && src.Port == a.Port {
			if mapped, e := stun.ParseResponse(buf[:n], tx); e == nil {
				candidates = append(candidates, mapped.String())
			}
		}
		_ = u.SetReadDeadline(time.Time{})
	}
	in := protocol.Intent{From: id.ID, To: target, Kind: "desktop", Nonce: protocol.RandomHex(16), IssuedAt: time.Now().Unix(), MediaCandidates: candidates}
	in.Sign(id.Private)
	var response protocol.SessionResponse
	if e = api.Do(ctx, "POST", "/v1/sessions", in, &response); e != nil {
		return e
	}
	b, e := media.NewBroker(media.BrokerConfig{Session: in.Nonce, Side: 0, Private: id.Private, Peer: pub, Candidates: p.Candidates.UDP, Ticket: response.Ticket, RelayUDP: mediaRelayAddr(cfg), RelayOnly: relayOnly, Send: func(p []byte, a *net.UDPAddr) error { _, e := u.WriteToUDP(p, a); return e }, Relay: func(c context.Context) (net.Conn, error) { return api.Relay(c, in.Nonce, "a", response.Ticket) }})
	if e != nil {
		return e
	}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 2048)
		for {
			n, a, e := u.ReadFromUDP(buf)
			if e != nil {
				return
			}
			b.Inject(buf[:n], a)
		}
	}()
	defer func() { u.Close(); <-readDone }()
	o.Host = false
	return runSession(ctx, dir, id, pub, b, nativeOptions(dir, cfg, o), api)
}
func (r *runtime) startDesktop(ev protocol.Event) error {
	if !r.cfg.DesktopEnabled {
		return errors.New("desktop hosting disabled locally")
	}
	pub, e := protocol.ParsePublic(ev.Peer.Device.PublicKey)
	if e != nil {
		return e
	}
	acl, e := identity.ReadACL(r.dir)
	if e != nil {
		return e
	}
	p := acl.Peers[ev.Peer.Device.ID]
	b, e := media.NewBroker(media.BrokerConfig{Session: ev.SessionID, Side: 1, Private: r.id.Private, Peer: pub, Candidates: ev.Intent.MediaCandidates, Ticket: ev.Ticket, RelayUDP: mediaRelayAddr(r.cfg), Send: func(p []byte, a *net.UDPAddr) error { _, e := r.udp.WriteToUDP(p, a); return e }, Relay: func(ctx context.Context) (net.Conn, error) { return r.api.Relay(ctx, ev.SessionID, "b", ev.Ticket) }})
	if e != nil {
		return e
	}
	r.mu.Lock()
	if len(r.mediaSessions) >= 1 {
		r.mu.Unlock()
		b.Close()
		return errors.New("one active desktop host session is permitted")
	}
	r.mediaSessions[ev.SessionID] = b
	r.mu.Unlock()
	r.launch(func() {
		defer func() { r.mu.Lock(); delete(r.mediaSessions, ev.SessionID); r.mu.Unlock() }()
		o := media.NativeOptions{Host: true, Control: p.Control, Clipboard: p.Clipboard, Audio: p.Audio, RequireHardware: true}
		ctx, cancel := context.WithTimeout(r.ctx, time.Hour)
		defer cancel()
		if e := runSession(ctx, r.dir, r.id, pub, b, nativeOptions(r.dir, r.cfg, o), r.api); e != nil && r.ctx.Err() == nil {
			slog.Warn("desktop host stopped", "error", e)
		}
	})
	return nil
}
