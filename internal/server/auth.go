package server

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"remotedesk.local/remotedesk/internal/protocol"
)

func sameSecret(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	jsonResponse(w, status, map[string]string{"error": msg})
}
func decode(b []byte, v any) error {
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var more any
	if e := d.Decode(&more); e != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, registration bool) (ed25519.PublicKey, []byte, bool) {
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, protocol.MaxBody))
	if e != nil {
		fail(w, 413, "request too large")
		return nil, nil, false
	}
	pub, e := protocol.ParsePublic(r.Header.Get("X-RD-Key"))
	if e != nil {
		fail(w, 401, "authentication required")
		return nil, nil, false
	}
	id := protocol.DeviceID(pub)
	if s.console.disabled(id) {
		fail(w, 403, "device disabled by coordinator")
		return nil, nil, false
	}
	if !registration {
		d, ok := s.store.Get(id)
		if !ok || d.Disabled || d.PublicKey != protocol.PublicString(pub) {
			fail(w, 401, "unknown device")
			return nil, nil, false
		}
	}
	ts := r.Header.Get("X-RD-Time")
	stamp, e := strconv.ParseInt(ts, 10, 64)
	nonce := r.Header.Get("X-RD-Nonce")
	if e != nil || !protocol.Fresh(stamp, time.Now()) || !protocol.ValidNonce(nonce) {
		fail(w, 401, "expired or invalid request")
		return nil, nil, false
	}
	signed := protocol.CanonicalRequest(r.Method, r.URL.RequestURI(), ts, nonce, protocol.PublicString(pub), body)
	if !protocol.Verify(pub, signed, r.Header.Get("X-RD-Signature")) {
		fail(w, 401, "invalid signature")
		return nil, nil, false
	}
	k := id + ":" + nonce
	if s.presence != nil {
		fresh, e := s.presence.Claim(r.Context(), k, 2*protocol.ClockWindow)
		if e != nil {
			fail(w, 503, "replay backend unavailable")
			return nil, nil, false
		}
		if !fresh {
			fail(w, 409, "replayed request")
			return nil, nil, false
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.replay[k]; ok {
		fail(w, 409, "replayed request")
		return nil, nil, false
	}
	if len(s.replay) >= 100000 {
		fail(w, 429, "authentication capacity reached")
		return nil, nil, false
	}
	s.replay[k] = time.Now().Add(2 * protocol.ClockWindow)
	return pub, body, true
}
