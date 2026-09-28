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
		if p[21] == 0 {
			s.relayRX.Add(uint64(len(p) - media.RelayHeader))
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
			if n, e := c.WriteToUDP(out, dst); e == nil && n == len(out) && out[21] == 2 {
				s.relayTX.Add(uint64(n - media.RelayHeader))
				s.relayPackets.Add(1)
			}
		}
	}
}

func (s *Server) relayStats() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	valid, paired := 0, 0
	for _, v := range s.sessions {
		if v.status.Kind != "desktop" || v.status.Closed || v.status.ExpiresAt < now.Unix() {
			continue
		}
		valid++
		if now.Sub(v.udpSeen[0]) < 5*time.Second && now.Sub(v.udpSeen[1]) < 5*time.Second {
			paired++
		}
	}
	state := "starting"
	if c, ok := s.components["UDP"]; ok && c.State == "ready" {
		state = "online"
	}
	if s.ctx.Err() != nil {
		state = "stopping"
	}
	return map[string]any{"status": state, "listen": s.cfg.STUN, "advertise": "configured at clients", "valid_grants": valid, "both_sides_seen": paired, "received_payload_bytes": s.relayRX.Load(), "forwarded_payload_bytes": s.relayTX.Load(), "forwarded_packets": s.relayPackets.Load()}
}
