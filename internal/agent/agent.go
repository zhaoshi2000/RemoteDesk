// Package agent runs identity, signaling, UDP reachability checks and authorized SSH tunnels.
package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"remotedesk.local/remotedesk/internal/apiclient"
	"remotedesk.local/remotedesk/internal/filetransfer"
	"remotedesk.local/remotedesk/internal/identity"
	"remotedesk.local/remotedesk/internal/media"
	"remotedesk.local/remotedesk/internal/protocol"
	"remotedesk.local/remotedesk/internal/stun"
	"remotedesk.local/remotedesk/internal/tunnel"
)

type probeState struct {
	key       ed25519.PublicKey
	peer      string
	nonce     string
	until     time.Time
	addresses []string
	success   bool
}
type runtime struct {
	ctx           context.Context
	dir           string
	id            *identity.Identity
	cfg           identity.Config
	api           *apiclient.Client
	tcp           net.Listener
	udp           *net.UDPConn
	stunAddr      *net.UDPAddr
	mu            sync.Mutex
	reflexive     string
	tx            [12]byte
	hasTx         bool
	probes        map[string]*probeState
	seen          map[string]time.Time
	wg            sync.WaitGroup
	slots         chan struct{}
	mediaSessions map[string]*media.Broker
}

func (r *runtime) launch(f func())                 { r.wg.Add(1); go func() { defer r.wg.Done(); f() }() }
func Run(parent context.Context, dir string) error { return run(parent, dir, false) }

// Service keeps identity/signaling/SSH online. User-writable configuration must
// never cause a LocalSystem process to execute a capture binary or serve files.
// Desktop/file hosting runs explicitly in the user's interactive session.
func RunService(parent context.Context, dir string) error { return run(parent, dir, true) }
func run(parent context.Context, dir string, service bool) error {
	id, cfg, e := identity.Load(dir)
	if e != nil {
		return e
	}
	if service {
		cfg.DesktopEnabled = false
		cfg.ReceiveDirectory = ""
		cfg.MediaExecutable = ""
		slog.Info("service mode: desktop/file hosting is confined to the interactive user process")
	}
	api, e := apiclient.New(id, cfg)
	if e != nil {
		return e
	}
	defer api.Close()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	tcp, e := net.Listen("tcp", cfg.ListenTCP)
	if e != nil {
		return fmt.Errorf("listen TCP: %w", e)
	}
	defer tcp.Close()
	ua, e := net.ResolveUDPAddr("udp4", cfg.ListenUDP)
	if e != nil {
		return e
	}
	udp, e := net.ListenUDP("udp4", ua)
	if e != nil {
		return fmt.Errorf("listen UDP: %w", e)
	}
	defer udp.Close()
	r := &runtime{ctx: ctx, dir: dir, id: id, cfg: cfg, api: api, tcp: tcp, udp: udp, probes: map[string]*probeState{}, seen: map[string]time.Time{}, slots: make(chan struct{}, 16), mediaSessions: map[string]*media.Broker{}}
	if cfg.STUN != "" {
		r.stunAddr, e = net.ResolveUDPAddr("udp4", cfg.STUN)
		if e != nil {
			return fmt.Errorf("resolve STUN: %w", e)
		}
	}
	r.launch(r.readUDP)
	r.refreshSTUN()
	if e = r.heartbeat(); e != nil {
		cancel()
		udp.Close()
		r.wg.Wait()
		return fmt.Errorf("initial heartbeat (register first): %w", e)
	}
	r.launch(r.acceptTCP)
	r.launch(r.events)
	r.launch(r.periodic)
	r.launch(func() { r.reportTelemetry(service) })
	slog.Info("agent online", "device_id", id.ID, "tcp", tcp.Addr().String(), "udp", udp.LocalAddr().String())
	<-ctx.Done()
	tcp.Close()
	udp.Close()
	r.wg.Wait()
	return nil
}
func localCandidates(addr net.Addr) []string {
	host, port, e := net.SplitHostPort(addr.String())
	if e != nil {
		return nil
	}
	bound := net.ParseIP(host)
	if bound != nil && !bound.IsUnspecified() {
		return []string{net.JoinHostPort(host, port)}
	}
	result := []string{}
	ips, e := net.InterfaceAddrs()
	if e != nil {
		return result
	}
	for _, a := range ips {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
			continue
		}
		result = append(result, net.JoinHostPort(ip.String(), port))
		if len(result) == 15 {
			break
		}
	}
	return result
}
func (r *runtime) candidates() protocol.CandidateSet {
	udp := localCandidates(r.udp.LocalAddr())
	r.mu.Lock()
	ref := r.reflexive
	r.mu.Unlock()
	if ref != "" {
		found := false
		for _, a := range udp {
			if a == ref {
				found = true
			}
		}
		if !found {
			udp = append(udp, ref)
		}
	}
	c := protocol.CandidateSet{DeviceID: r.id.ID, TCP: localCandidates(r.tcp.Addr()), UDP: udp, IssuedAt: time.Now().Unix()}
	c.Sign(r.id.Private)
	return c
}
func (r *runtime) heartbeat() error {
	ctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
	defer cancel()
	return r.api.Do(ctx, "POST", "/v1/heartbeat", r.candidates(), nil)
}
func (r *runtime) periodic() {
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	nat := time.NewTicker(10 * time.Second)
	defer nat.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-heartbeat.C:
			if e := r.heartbeat(); e != nil && r.ctx.Err() == nil {
				slog.Warn("heartbeat failed", "error", e)
			}
			r.mu.Lock()
			for k, p := range r.probes {
				if time.Now().After(p.until) {
					delete(r.probes, k)
				}
			}
			r.mu.Unlock()
		case <-nat.C:
			r.refreshSTUN()
		}
	}
}
func (r *runtime) refreshSTUN() {
	if r.stunAddr == nil {
		return
	}
	b, tx := stun.Request()
	r.mu.Lock()
	r.tx = tx
	r.hasTx = true
	r.mu.Unlock()
	_, _ = r.udp.WriteToUDP(b, r.stunAddr)
}
func (r *runtime) acceptTCP() {
	for {
		c, e := r.tcp.Accept()
		if e != nil {
			return
		}
		select {
		case r.slots <- struct{}{}:
			r.launch(func() {
				defer func() { <-r.slots }()
				ctx, cancel := context.WithTimeout(r.ctx, time.Hour)
				defer cancel()
				stop := context.AfterFunc(ctx, func() { c.Close() })
				defer stop()
				e := r.servePeer(ctx, c, "")
				if e != nil && r.ctx.Err() == nil {
					slog.Debug("direct SSH tunnel closed", "error", e)
				}
			})
		default:
			c.Close()
		}
	}
}
func (r *runtime) events() {
	cursor := uint64(0)
	backoff := 500 * time.Millisecond
	for {
		var events []protocol.Event
		e := r.api.Do(r.ctx, "GET", "/v1/events?after="+strconv.FormatUint(cursor, 10), nil, &events)
		if e != nil {
			if r.ctx.Err() != nil {
				return
			}
			slog.Warn("signaling disconnected", "retry_ms", backoff.Milliseconds())
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 10*time.Second {
				backoff = 10 * time.Second
			}
			continue
		}
		backoff = 500 * time.Millisecond
		for _, ev := range events {
			if ev.Seq > cursor {
				cursor = ev.Seq
			}
			if e = r.handle(ev); e != nil {
				slog.Debug("session event rejected", "error", e)
			}
		}
	}
}
func (r *runtime) handle(ev protocol.Event) error {
	pub, e := protocol.ParsePublic(ev.Peer.Device.PublicKey)
	if e != nil {
		return e
	}
	if ev.Peer.Device.ID != protocol.DeviceID(pub) || !ev.Peer.Candidates.Verify(pub) || ev.SessionID != ev.Intent.Nonce {
		return errors.New("invalid signed peer metadata")
	}
	if ev.Intent.From == r.id.ID {
		if ev.Intent.Kind != "probe" || !ev.Intent.Verify(r.id.Public) || ev.Intent.To != ev.Peer.Device.ID {
			return errors.New("invalid originating probe event")
		}
	} else if ev.Intent.To != r.id.ID || ev.Intent.From != ev.Peer.Device.ID || !ev.Intent.Verify(pub) {
		return errors.New("invalid incoming intent")
	}
	if !identity.Allowed(r.dir, ev.Peer.Device.PublicKey, ev.Intent.Kind) {
		return errors.New("peer is not locally authorized for this capability")
	}
	if _, ok := r.seen[ev.SessionID]; ok {
		return nil
	}
	for k, t := range r.seen {
		if time.Since(t) > 2*time.Minute {
			delete(r.seen, k)
		}
	}
	if len(r.seen) >= 128 {
		return errors.New("local session capacity reached")
	}
	r.seen[ev.SessionID] = time.Now()
	if ev.Intent.Kind == "desktop" {
		return r.startDesktop(ev)
	}
	if ev.Intent.Kind == "probe" {
		p := &probeState{key: pub, peer: ev.Peer.Device.ID, nonce: protocol.RandomHex(16), until: time.Now().Add(12 * time.Second), addresses: ev.Peer.Candidates.UDP}
		r.mu.Lock()
		r.probes[ev.SessionID] = p
		r.mu.Unlock()
		r.launch(func() { r.sendProbes(ev.SessionID, p) })
		return nil
	}
	select {
	case r.slots <- struct{}{}:
		r.launch(func() { defer func() { <-r.slots }(); r.acceptRelay(ev) })
		return nil
	default:
		return errors.New("local tunnel limit reached")
	}
}
func (r *runtime) acceptRelay(ev protocol.Event) {
	ctx, cancel := context.WithTimeout(r.ctx, time.Hour)
	defer cancel()
	raw, e := r.api.Relay(ctx, ev.SessionID, "b", ev.Ticket)
	if e != nil {
		slog.Debug("relay connection failed", "error", e)
		return
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	e = r.servePeer(ctx, raw, ev.Peer.Device.PublicKey)
	if e != nil && r.ctx.Err() == nil {
		slog.Debug("relayed SSH tunnel closed", "error", e)
	}
}
func (r *runtime) sendProbes(session string, p *probeState) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		r.mu.Lock()
		done := p.success || time.Now().After(p.until)
		r.mu.Unlock()
		if done {
			return
		}
		packet := protocol.ProbePacket{Session: session, From: r.id.ID, To: p.peer, Kind: "ping", Nonce: p.nonce, IssuedAt: time.Now().Unix()}
		packet.Sign(r.id.Private)
		b, _ := json.Marshal(packet)
		b = append([]byte("RDP1"), b...)
		for _, a := range p.addresses {
			addr, e := net.ResolveUDPAddr("udp4", a)
			if e == nil {
				_, _ = r.udp.WriteToUDP(b, addr)
			}
		}
		select {
		case <-r.ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (r *runtime) readUDP() {
	buf := make([]byte, 2048)
	for {
		n, addr, e := r.udp.ReadFromUDP(buf)
		if e != nil {
			return
		}
		b := buf[:n]
		if n >= 21 && (string(b[:4]) == "RDM2" || string(b[:4]) == "RDR2") {
			sid := hex.EncodeToString(b[4:20])
			r.mu.Lock()
			m := r.mediaSessions[sid]
			r.mu.Unlock()
			if m != nil {
				m.Inject(b, addr)
			}
			continue
		}
		if stun.IsSTUN(b) {
			r.mu.Lock()
			tx, ok := r.tx, r.hasTx
			r.mu.Unlock()
			if ok && r.stunAddr != nil && addr.IP.Equal(r.stunAddr.IP) && addr.Port == r.stunAddr.Port {
				mapped, e := stun.ParseResponse(b, tx)
				if e == nil {
					r.mu.Lock()
					r.reflexive = mapped.String()
					r.hasTx = false
					r.mu.Unlock()
				}
			}
			continue
		}
		if n < 5 || n > 1200 || string(b[:4]) != "RDP1" {
			continue
		}
		var p protocol.ProbePacket
		if json.Unmarshal(b[4:], &p) != nil || p.To != r.id.ID {
			continue
		}
		r.mu.Lock()
		state, ok := r.probes[p.Session]
		valid := ok && time.Now().Before(state.until)
		r.mu.Unlock()
		if !valid || p.From != state.peer || !p.Verify(state.key) {
			continue
		}
		if p.Kind == "ping" {
			reply := protocol.ProbePacket{Session: p.Session, From: r.id.ID, To: p.From, Kind: "pong", Nonce: p.Nonce, IssuedAt: time.Now().Unix()}
			reply.Sign(r.id.Private)
			data, _ := json.Marshal(reply)
			_, _ = r.udp.WriteToUDP(append([]byte("RDP1"), data...), addr)
			continue
		}
		if p.Nonce != state.nonce {
			continue
		}
		r.mu.Lock()
		report := !state.success
		state.success = true
		r.mu.Unlock()
		if report {
			session, address := p.Session, addr.String()
			r.launch(func() {
				ctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
				defer cancel()
				e := r.api.Do(ctx, "POST", "/v1/probe-result", protocol.ProbeResult{SessionID: session, Address: address, Success: true}, nil)
				if e == nil {
					slog.Info("signed UDP path confirmed", "peer_id", state.peer, "session", session)
				}
			})
		}
	}
}

func (r *runtime) servePeer(ctx context.Context, c net.Conn, expected string) error {
	return tunnel.ServeMultiplex(ctx, c, r.id, func(key, kind string) bool {
		return (expected == "" || key == expected) && identity.Allowed(r.dir, key, kind) && (kind != "file" || r.cfg.ReceiveDirectory != "")
	}, r.cfg.SSHPort, func(c net.Conn, key string) error {
		return filetransfer.Serve(ctx, c, r.cfg.ReceiveDirectory, key, 1<<20)
	})
}
