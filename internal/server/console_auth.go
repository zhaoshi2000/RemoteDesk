package server

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"remotedesk.local/remotedesk/internal/protocol"
	"strings"
	"time"
)

const adminCookie = "__Host-remotedesk-admin"

func digestToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, e := url.Parse(o)
	return e == nil && u.Scheme == "https" && u.Host == r.Host && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
func adminJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		fail(w, 415, "JSON content type required")
		return false
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if e != nil {
		fail(w, 413, "request too large")
		return false
	}
	if decode(b, v) != nil {
		fail(w, 400, "invalid JSON body")
		return false
	}
	return true
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "POST required")
		return
	}
	if r.TLS == nil {
		fail(w, 400, "HTTPS required")
		return
	}
	if !sameOrigin(r) {
		fail(w, 403, "cross-origin login rejected")
		return
	}
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		host = r.RemoteAddr
	}
	now := time.Now()
	s.console.mu.Lock()
	for k, v := range s.console.failures {
		if now.After(v.Until) {
			delete(s.console.failures, k)
		}
	}

	b := s.console.failures[host]
	if b.Count >= 5 || len(s.console.failures) >= 4096 {
		s.console.mu.Unlock()
		w.Header().Set("Retry-After", "300")
		fail(w, 429, "login temporarily rate limited")
		return
	}
	if b.Until.IsZero() {
		b.Until = now.Add(5 * time.Minute)
	}
	b.Count++
	s.console.failures[host] = b
	s.console.mu.Unlock()
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if !readAdmin(w, r, &req) {
		return
	}
	credential := req.Password
	if req.Token != "" {
		credential = req.Token
	}
	actor, role := s.tokenUser(credential)
	if actor == "" {
		fail(w, 401, "invalid administrator credentials")
		return
	}

	token, csrf := protocol.RandomHex(32), protocol.RandomHex(32)
	s.console.mu.Lock()
	delete(s.console.failures, host)
	s.console.mu.Unlock()
	s.mu.Lock()
	for k, v := range s.loginSessions {
		if now.After(v.Expires) {
			delete(s.loginSessions, k)
		}
	}
	if len(s.loginSessions) >= 1024 {
		s.mu.Unlock()
		fail(w, 429, "session limit")
		return
	}
	s.loginSessions[digestToken(token)] = loginSession{User: actor, Expires: now.Add(8 * time.Hour), CSRF: csrf}
	s.mu.Unlock()
	if e := s.console.mutateAs(actor, "login", host, func(*adminData) error { return nil }); e != nil {
		s.mu.Lock()
		delete(s.loginSessions, digestToken(token))
		s.mu.Unlock()
		fail(w, 500, "could not persist login audit")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: token, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: 8 * 3600})
	jsonResponse(w, 200, map[string]any{"username": actor, "user": actor, "role": role, "csrf": csrf, "expires_at": now.Add(8 * time.Hour)})
}
