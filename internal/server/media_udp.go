package server

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"net"
	"remotedesk.local/remotedesk/internal/media"
	"remotedesk.local/remotedesk/internal/stun"
	"time"
)

// ServeMediaUDP multiplexes bounded STUN binding and authenticated opaque QUIC relay.
// UDP endpoints are learned only after a valid per-session MAC and anti-replay check.
func (s *Server) ServeMediaUDP(ctx context.Context, c *net.UDPConn) error {
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	buf := make([]byte, 2048)
	rates := map[string]bucket{}
	for {
		n, addr, e := c.ReadFromUDP(buf)
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		p := buf[:n]
		if stun.IsSTUN(p) {
			now := time.Now()
			if len(rates) >= 4096 {
				for k, v := range rates {
					if now.Sub(v.start) > time.Second {
						delete(rates, k)
					}
				}
			}
			key := addr.IP.String()
			r, ok := rates[key]
			if !ok && len(rates) >= 4096 {
				continue
			}
			if now.Sub(r.start) > time.Second {
				r = bucket{start: now}
			}
			r.count++
			rates[key] = r
			if r.count <= 20 {
				if out, e := stun.Response(p, addr); e == nil {
					_, _ = c.WriteToUDP(out, addr)
				}
			}
			continue
		}
		if len(p) < media.RelayHeader || len(p) > media.RelayHeader+media.MaxDatagram || string(p[:4]) != "RDR2" || p[20] > 1 || p[21] > 1 {
			continue
		}
		sid := hex.EncodeToString(p[4:20])
		side := int(p[20])
		now := time.Now()
		s.mu.Lock()
		v, ok := s.sessions[sid]
		if !ok || v.status.Kind != "desktop" || v.status.Closed || now.Unix() > v.status.ExpiresAt || !media.VerifyRelay(p, v.ticket[side]) || !v.udpWindow[side].Accept(binary.BigEndian.Uint64(p[22:30])) {
			s.mu.Unlock()
			continue
		}
		// 64 Mbit/s aggregate/session, bounded one-second burst. Avoid an unauthenticated open relay.
		if now.Sub(v.budgetAt) >= time.Second {
			v.budgetAt = now
			v.budget = 8_000_000
		}
		if v.budget < len(p) {
			s.mu.Unlock()
			continue
		}
		v.budget -= len(p)
		v.udpAddr[side] = addr
		v.udpSeen[side] = now
		sessionBytes, _ := media.SessionBytes(sid)
		var out []byte
		var dst *net.UDPAddr
		if p[21] == 1 {
			v.udpSeq[side]++
			out = media.RelayPacket(sessionBytes, byte(side), 3, v.udpSeq[side], v.ticket[side], nil)
			dst = addr
		} else if v.udpAddr[1-side] != nil && now.Sub(v.udpSeen[1-side]) < 5*time.Second {
			other := 1 - side
			v.udpSeq[other]++
			out = media.RelayPacket(sessionBytes, byte(other), 2, v.udpSeq[other], v.ticket[other], p[media.RelayHeader:])
			dst = v.udpAddr[other]
			v.status.Paired = true
			v.status.ExpiresAt = now.Add(time.Hour).Unix()
		}
		s.mu.Unlock()
		if out != nil {
			_, _ = c.WriteToUDP(out, dst)
		}
	}
}
