package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"remotedesk.local/remotedesk/internal/apiclient"
	"remotedesk.local/remotedesk/internal/bridge"
	"remotedesk.local/remotedesk/internal/identity"
	"remotedesk.local/remotedesk/internal/protocol"
	"remotedesk.local/remotedesk/internal/tunnel"
)

func DialPeer(ctx context.Context, dir, target string, relayOnly bool) (net.Conn, string, error) {
	return dialCapability(ctx, dir, target, relayOnly, "ssh")
}
func DialFile(ctx context.Context, dir, target string, relayOnly bool) (net.Conn, string, error) {
	return dialCapability(ctx, dir, target, relayOnly, "file")
}
func dialCapability(ctx context.Context, dir, target string, relayOnly bool, kind string) (net.Conn, string, error) {
	id, cfg, e := identity.Load(dir)
	if e != nil {
		return nil, "", e
	}
	api, e := apiclient.New(id, cfg)
	if e != nil {
		return nil, "", e
	}
	defer api.Close()
	p, e := api.Peer(ctx, target)
	if e != nil {
		return nil, "", e
	}
	pub, e := protocol.ParsePublic(p.Device.PublicKey)
	if e != nil || p.Device.ID != target || protocol.DeviceID(pub) != target || !p.Candidates.Verify(pub) {
		return nil, "", errors.New("invalid or stale signed target metadata")
	}
	if !p.Online {
		return nil, "", errors.New("target offline")
	}
	if !identity.Allowed(dir, p.Device.PublicKey, kind) {
		return nil, "", errors.New("target key is not explicitly authorized for this capability")
	}
	if !relayOnly {
		directCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		for _, a := range p.Candidates.TCP {
			if directCtx.Err() != nil {
				break
			}
			raw, e := (&net.Dialer{Timeout: 700 * time.Millisecond}).DialContext(directCtx, "tcp", a)
			if e != nil {
				continue
			}
			secure, e := channelClient(directCtx, raw, id, p.Device.PublicKey, kind)
			if e == nil {
				cancel()
				return secure, "Direct TLS/TCP (not UDP QUIC)", nil
			}
		}
		cancel()
	}
	session, e := api.CreateSession(ctx, target, kind)
	if e != nil {
		return nil, "", e
	}
	raw, e := api.Relay(ctx, session.ID, "a", session.Ticket)
	if e != nil {
		return nil, "", e
	}
	secure, e := channelClient(ctx, raw, id, p.Device.PublicKey, kind)
	if e != nil {
		return nil, "", e
	}
	return secure, "Relay / end-to-end TLS 1.3", nil
}
func ListenLoopback(address string) (net.Listener, error) {
	h, _, e := net.SplitHostPort(address)
	if e != nil {
		return nil, e
	}
	ip := net.ParseIP(h)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("local SSH forwarding listener must bind a loopback IP")
	}
	return net.Listen("tcp", address)
}
func Forward(parent context.Context, ln net.Listener, dir, target string, relayOnly bool) error {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	defer ln.Close()
	slots := make(chan struct{}, 8)
	var wg sync.WaitGroup
	defer func() { cancel(); ln.Close(); wg.Wait() }()
	for {
		local, e := ln.Accept()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		select {
		case slots <- struct{}{}:
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				defer local.Close()
				connCtx, cancel := context.WithTimeout(ctx, time.Hour)
				defer cancel()
				peer, mode, e := DialPeer(connCtx, dir, target, relayOnly)
				if e != nil {
					slog.Warn("SSH connection failed", "error", e)
					return
				}
				slog.Info("SSH tunnel connected", "peer_id", target, "path", mode)
				_, _, _ = bridge.Pipe(connCtx, local, peer, 0)
			}()
		default:
			local.Close()
		}
	}
}
func Probe(ctx context.Context, dir, target string) (protocol.SessionStatus, error) {
	id, cfg, e := identity.Load(dir)
	if e != nil {
		return protocol.SessionStatus{}, e
	}
	api, e := apiclient.New(id, cfg)
	if e != nil {
		return protocol.SessionStatus{}, e
	}
	defer api.Close()
	p, e := api.Peer(ctx, target)
	if e != nil {
		return protocol.SessionStatus{}, e
	}
	if !identity.Allowed(dir, p.Device.PublicKey, "probe") {
		return protocol.SessionStatus{}, errors.New("target not trusted for UDP probing")
	}
	s, e := api.CreateSession(ctx, target, "probe")
	if e != nil {
		return protocol.SessionStatus{}, e
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var status protocol.SessionStatus
	for {
		status, e = api.Status(ctx, s.ID)
		if e != nil {
			return status, e
		}
		if status.AReachable && status.BReachable {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, fmt.Errorf("UDP probe did not confirm both directions: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

func channelClient(ctx context.Context, c net.Conn, id *identity.Identity, key, kind string) (net.Conn, error) {
	if kind == "file" {
		return tunnel.FileClient(ctx, c, id, key)
	}
	return tunnel.Client(ctx, c, id, key)
}
