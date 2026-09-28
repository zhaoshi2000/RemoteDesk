package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"remotedesk.local/remotedesk/internal/bridge"
)

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }
func (c *bufferedConn) CloseWrite() error {
	if w, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return w.CloseWrite()
	}
	return c.Conn.Close()
}
func (s *Server) relay(w http.ResponseWriter, r *http.Request) {
	p := strings.Split(strings.TrimPrefix(r.RequestURI, "/v1/relay/"), "/")
	if len(p) != 2 || (p[1] != "a" && p[1] != "b") {
		fail(w, 400, "invalid relay path")
		return
	}
	side := 0
	if p[1] == "b" {
		side = 1
	}
	h, ok := w.(http.Hijacker)
	if !ok {
		fail(w, 505, "relay requires HTTP/1.1")
		return
	}
	ticket := sha256.Sum256([]byte(bearer(r)))
	s.mu.Lock()
	v, ok := s.sessions[p[0]]
	if !ok || (v.status.Kind != "ssh" && v.status.Kind != "desktop" && v.status.Kind != "file") || v.status.Closed || time.Now().Unix() > v.status.ExpiresAt || v.used[side] || subtle.ConstantTimeCompare(ticket[:], v.ticket[side][:]) != 1 {
		s.mu.Unlock()
		fail(w, 403, "relay ticket invalid or expired")
		return
	}
	v.used[side] = true
	s.mu.Unlock()
	conn, rw, e := h.Hijack()
	if e != nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, e = fmt.Fprint(rw, "HTTP/1.1 200 Connection Established\r\n\r\n")
	if e == nil {
		e = rw.Flush()
	}
	_ = conn.SetDeadline(time.Time{})
	if e != nil {
		conn.Close()
		return
	}
	wrapped := &bufferedConn{conn, rw.Reader}
	s.mu.Lock()
	if v.status.Closed {
		s.mu.Unlock()
		wrapped.Close()
		return
	}
	v.conn[side] = wrapped
	if v.conn[0] != nil && v.conn[1] != nil && !v.paired {
		v.paired = true
		v.status.Paired = true
		v.status.ExpiresAt = time.Now().Add(time.Hour).Unix()
		a, b := v.conn[0], v.conn[1]
		go func() {
			ctx, cancel := context.WithTimeout(s.ctx, time.Hour)
			defer cancel()
			_, _, _ = bridge.Pipe(ctx, a, b, 1<<30)
			s.mu.Lock()
			v.status.Closed = true
			s.mu.Unlock()
		}()
	}
	s.mu.Unlock()
}
