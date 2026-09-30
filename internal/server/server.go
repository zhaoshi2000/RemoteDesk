// Package server contains the authenticated coordinator and opaque TLS stream relay.
package server

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"remotedesk.local/remotedesk/internal/monitor"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"remotedesk.local/remotedesk/internal/media"
	"remotedesk.local/remotedesk/internal/protocol"
	"remotedesk.local/remotedesk/internal/store"
)

//go:embed static/*
var assets embed.FS

type live struct {
	candidates protocol.CandidateSet
	seen       time.Time
}
type bucket struct {
	start time.Time
	count int
}
type session struct {
	status    protocol.SessionStatus
	intent    protocol.Intent
	created   time.Time
	ticket    [2][32]byte
	used      [2]bool
	conn      [2]net.Conn
	paired    bool
	udpAddr   [2]*net.UDPAddr
	udpWindow [2]media.Window
	udpSeq    [2]uint64
	udpSeen   [2]time.Time
	budgetAt  time.Time
	budget    int
}
type Server struct {
	cfg           Config
	store         store.Registry
	mu            sync.Mutex
	online        map[string]live
	replay        map[string]time.Time
	events        map[string][]protocol.Event
	sessions      map[string]*session
	rates         map[string]bucket
	seq           uint64
	ctx           context.Context
	cancel        context.CancelFunc
	static        http.Handler
	adminState    *catalog
	presence      Presence
	loginSessions map[string]loginSession
	console       *administration
	monitor       *monitor.Collector
	started       time.Time
	requests      atomic.Uint64
	relayRX       atomic.Uint64
	relayTX       atomic.Uint64
	relayPackets  atomic.Uint64
	components    map[string]ServiceStatus
	telemetry     map[string]DeviceTelemetry
}

func New(cfg Config, st store.Registry) (*Server, error) {
	return NewWithBackends(cfg, st, nil, nil)
}
func NewWithBackends(cfg Config, st store.Registry, presence Presence, state StateBackend) (*Server, error) {
	if len(cfg.AdminToken) < 32 || len(cfg.EnrollmentToken) < 32 {
		return nil, errors.New("admin and enrollment tokens must be at least 32 characters")
	}
	ctx, cancel := context.WithCancel(context.Background())
	sub, _ := fs.Sub(assets, "static")
	s := &Server{cfg: cfg, store: st, online: map[string]live{}, replay: map[string]time.Time{}, events: map[string][]protocol.Event{}, sessions: map[string]*session{}, rates: map[string]bucket{}, ctx: ctx, cancel: cancel, seq: uint64(time.Now().UnixNano()), static: http.FileServer(http.FS(sub))}
	s.presence = presence
	s.loginSessions = map[string]loginSession{}
	s.telemetry = map[string]DeviceTelemetry{}
	if state == nil && cfg.Store != "" {
		state = FileState{Path: filepath.Join(filepath.Dir(cfg.Store), "management.json")}
	}
	var e error
	s.adminState, e = newCatalog(state)
	if e != nil {
		cancel()
		return nil, e
	}
	if cfg.AdminDirectory != "" {
		if st, e := os.Stat(filepath.Join(cfg.AdminDirectory, "index.html")); e != nil || !st.Mode().IsRegular() {
			cancel()
			return nil, errors.New("admin_directory lacks built index.html")
		}
		s.static = http.FileServer(http.Dir(cfg.AdminDirectory))
	}
	s.console, e = newAdministration(cfg.Store)
	if e != nil {
		cancel()
		return nil, e
	}
	s.started = time.Now().UTC()
	s.monitor = monitor.New(filepath.Dir(cfg.Store))
	s.monitor.Collect()
	s.components = map[string]ServiceStatus{
		"HTTPS": {Name: "HTTPS / API / Admin", State: "starting", Listen: cfg.Listen, Detail: "HTTPS listener has not yet reported readiness"},
		"UDP":   {Name: "STUN / media relay", State: "starting", Listen: cfg.STUN, Detail: "UDP listener has not yet reported readiness"},
	}
	go s.collectMetrics()
	go s.maintenance()
	return s, nil
}
func (s *Server) Close() {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.sessions {
		for _, c := range v.conn {
			if c != nil {
				c.Close()
			}
		}
	}
}
func (s *Server) maintenance() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-t.C:
			s.mu.Lock()
			for k, v := range s.telemetry {
				if now.Sub(v.ReceivedAt) > time.Hour {
					delete(s.telemetry, k)
				}
			}
			for k, v := range s.replay {
				if now.After(v) {
					delete(s.replay, k)
				}
			}
			for k, v := range s.rates {
				if now.Sub(v.start) > 2*time.Minute {
					delete(s.rates, k)
				}
			}
			for id, ev := range s.events {
				kept := ev[:0]
				for _, e := range ev {
					if now.Sub(time.Unix(e.Intent.IssuedAt, 0)) < 2*protocol.ClockWindow {
						kept = append(kept, e)
					}
				}
				if len(kept) == 0 {
					delete(s.events, id)
				} else {
					s.events[id] = kept
				}
			}
			for k, v := range s.sessions {
				if now.Unix() > v.status.ExpiresAt && !v.status.Closed {
					v.status.Closed = true
					for _, c := range v.conn {
						if c != nil {
							c.Close()
						}
					}
				}
				if v.status.Closed && now.Sub(v.created) > 3*time.Minute {
					delete(s.sessions, k)
				}
			}
			s.mu.Unlock()
		}
	}
}
func (s *Server) rateOK(addr string) bool {
	host, _, e := net.SplitHostPort(addr)
	if e != nil {
		host = addr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	b, ok := s.rates[host]
	if !ok && len(s.rates) >= 4096 {
		return false
	}
	if now.Sub(b.start) >= time.Second {
		b = bucket{start: now}
	}
	b.count++
	s.rates[host] = b
	return b.count <= 120
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if !s.rateOK(r.RemoteAddr) {
		fail(w, 429, "rate limit exceeded")
		return
	}
	if r.Method == "CONNECT" && strings.HasPrefix(r.RequestURI, "/v1/relay/") {
		s.relay(w, r)
		return
	}
	if r.URL.Path == "/v1/releases/latest" {
		s.latestRelease(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/admin/") {
		s.manage(w, r)
		return
	}
	if r.URL.Path == "/healthz" && r.Method == "GET" {
		jsonResponse(w, 200, map[string]any{"ok": true, "protocol": protocol.Version})
		return
	}
	if r.URL.Path == "/" || r.URL.Path == "/admin" {
		http.Redirect(w, r, "/admin/", http.StatusTemporaryRedirect)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/") && r.Method == "GET" {
		http.StripPrefix("/admin", s.static).ServeHTTP(w, r)
		return
	}

	reg := r.URL.Path == "/v1/register" && r.Method == "POST"
	if reg && !s.console.registrationOpen() {
		fail(w, 403, "new device registration is disabled")
		return
	}
	if reg && !sameSecret(bearer(r), s.cfg.EnrollmentToken) {
		fail(w, 401, "invalid enrollment token")
		return
	}
	pub, body, ok := s.authenticate(w, r, reg)
	if !ok {
		return
	}
	id := protocol.DeviceID(pub)
	switch {
	case reg:
		var req struct {
			Name string `json:"name"`
		}
		if decode(body, &req) != nil || len([]rune(req.Name)) < 1 || len([]rune(req.Name)) > 100 || strings.IndexFunc(req.Name, unicode.IsControl) >= 0 {
			fail(w, 400, "invalid device name")
			return
		}
		d, e := s.store.Register(protocol.Device{ID: id, Name: req.Name, PublicKey: protocol.PublicString(pub), RegisteredAt: time.Now().Unix()})
		if e != nil {
			fail(w, 409, e.Error())
			return
		}
		jsonResponse(w, 200, d)
	case r.Method == "POST" && r.URL.Path == "/v1/telemetry":
		s.acceptTelemetry(w, r, id, body)
	case r.Method == "POST" && r.URL.Path == "/v1/heartbeat":
		var c protocol.CandidateSet
		if decode(body, &c) != nil || !c.Verify(pub) {
			fail(w, 400, "invalid signed candidates")
			return
		}
		s.mu.Lock()
		s.online[id] = live{c, time.Now()}
		s.mu.Unlock()
		if s.presence != nil {
			if e := s.presence.Put(r.Context(), id, c, time.Now().Unix()); e != nil {
				fail(w, 503, "presence backend unavailable")
				return
			}
		}
		jsonResponse(w, 200, map[string]any{"ok": true, "server_time": time.Now().Unix()})
	case r.Method == "GET" && r.URL.Path == "/v1/peer":
		p, ok := s.peer(r.URL.Query().Get("id"))
		if !ok {
			fail(w, 404, "device unavailable")
			return
		}
		jsonResponse(w, 200, p)
	case r.Method == "POST" && r.URL.Path == "/v1/sessions":
		var in protocol.Intent
		if decode(body, &in) != nil || !in.Verify(pub) {
			fail(w, 400, "invalid session intent")
			return
		}
		s.createSession(w, in)
	case r.Method == "GET" && r.URL.Path == "/v1/events":
		s.poll(w, r, id)
	case r.Method == "POST" && r.URL.Path == "/v1/probe-result":
		var pr protocol.ProbeResult
		if decode(body, &pr) != nil || !protocol.ValidAddress(pr.Address) {
			fail(w, 400, "invalid probe result")
			return
		}
		s.mu.Lock()
		v, ok := s.sessions[pr.SessionID]
		if !ok || v.status.Kind != "probe" || v.status.Closed || (id != v.status.From && id != v.status.To) {
			s.mu.Unlock()
			fail(w, 404, "session unavailable")
			return
		}
		if id == v.status.From {
			v.status.AReachable = pr.Success
		} else {
			v.status.BReachable = pr.Success
		}
		s.mu.Unlock()
		jsonResponse(w, 200, map[string]bool{"ok": true})
	case r.Method == "GET" && r.URL.Path == "/v1/session-status":
		s.mu.Lock()
		v, ok := s.sessions[r.URL.Query().Get("id")]
		if !ok || (v.status.From != id && v.status.To != id) {
			s.mu.Unlock()
			fail(w, 404, "session unavailable")
			return
		}
		status := v.status
		s.mu.Unlock()
		jsonResponse(w, 200, status)
	default:
		fail(w, 404, "unknown endpoint")
	}
}
func (s *Server) peer(id string) (protocol.Peer, bool) {
	d, ok := s.store.Get(id)
	if !ok {
		return protocol.Peer{}, false
	}
	s.mu.Lock()
	v := s.online[id]
	s.mu.Unlock()
	if s.presence != nil {
		ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
		c, seen, ok := s.presence.Get(ctx, id)
		cancel()
		if ok {
			v = live{c, time.Unix(seen, 0)}
		} else {
			v = live{}
		}
	}
	return protocol.Peer{Device: d, Candidates: v.candidates, Online: !d.Disabled && !s.console.disabled(id) && !v.seen.IsZero() && time.Since(v.seen) < 20*time.Second, LastSeen: v.seen.Unix()}, true
}
func (s *Server) push(id string, e protocol.Event) {
	s.seq++
	e.Seq = s.seq
	ev := s.events[id]
	if len(ev) >= 64 {
		ev = ev[len(ev)-63:]
	}
	s.events[id] = append(ev, e)
}
func (s *Server) createSession(w http.ResponseWriter, in protocol.Intent) {
	from, ok := s.peer(in.From)
	if !ok || !from.Online {
		fail(w, 409, "source agent must be online")
		return
	}
	to, ok := s.peer(in.To)
	if !ok || !to.Online {
		fail(w, 404, "target unavailable")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[in.Nonce]; ok {
		fail(w, 409, "replayed session intent")
		return
	}
	if len(s.sessions) >= s.adminState.maxSessions() {
		fail(w, 429, "session capacity reached")
		return
	}
	count := 0
	for _, v := range s.sessions {
		if !v.status.Closed && (v.status.From == in.From || v.status.To == in.To) {
			count++
		}
	}
	if count >= 32 {
		fail(w, 429, "device session limit reached")
		return
	}
	now := time.Now()
	expires := now.Add(40 * time.Second).Unix()
	v := &session{status: protocol.SessionStatus{ID: in.Nonce, Kind: in.Kind, From: in.From, To: in.To, ExpiresAt: expires}, intent: in, created: now}
	a, b := "", ""
	if in.Kind == "ssh" || in.Kind == "desktop" || in.Kind == "file" {
		a, b = protocol.RandomHex(32), protocol.RandomHex(32)
		v.ticket[0] = sha256.Sum256([]byte(a))
		v.ticket[1] = sha256.Sum256([]byte(b))
	}
	s.sessions[in.Nonce] = v
	s.push(in.To, protocol.Event{SessionID: in.Nonce, Intent: in, Peer: from, Ticket: b})
	if in.Kind == "probe" {
		s.push(in.From, protocol.Event{SessionID: in.Nonce, Intent: in, Peer: to})
	}
	jsonResponse(w, 200, protocol.SessionResponse{ID: in.Nonce, Ticket: a, ExpiresAt: expires})
}
func (s *Server) poll(w http.ResponseWriter, r *http.Request, id string) {
	after, e := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	if e != nil {
		fail(w, 400, "invalid cursor")
		return
	}
	deadline := time.NewTimer(18 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		res := []protocol.Event{}
		for _, ev := range s.events[id] {
			if ev.Seq > after {
				res = append(res, ev)
			}
		}
		s.mu.Unlock()
		if len(res) > 0 {
			jsonResponse(w, 200, res)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			fail(w, 503, "server stopping")
			return
		case <-deadline.C:
			jsonResponse(w, 200, res)
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) LogReady() {
	slog.Info("RemoteDesk coordinator ready", "listen", s.cfg.Listen, "stun", s.cfg.STUN)
}
