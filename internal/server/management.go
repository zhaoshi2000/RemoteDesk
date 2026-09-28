package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"remotedesk.local/remotedesk/internal/identity"
	"remotedesk.local/remotedesk/internal/protocol"
	"remotedesk.local/remotedesk/internal/update"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Presence combines TTL online records and atomic request nonce claims. Redis
// implementations fail closed; this does not make the in-process signal queue distributed.
type Presence interface {
	Put(context.Context, string, protocol.CandidateSet, int64) error
	Get(context.Context, string) (protocol.CandidateSet, int64, bool)
	Claim(context.Context, string, time.Duration) (bool, error)
}
type StateBackend interface {
	Load(context.Context) ([]byte, error)
	Save(context.Context, []byte) error
}
type FileState struct{ Path string }

func (f FileState) Load(context.Context) ([]byte, error) {
	b, e := os.ReadFile(f.Path)
	if os.IsNotExist(e) {
		return nil, nil
	}
	return b, e
}
func (f FileState) Save(_ context.Context, b []byte) error {
	return identity.WriteAtomic(f.Path, b, 0600)
}

type User struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Disabled  bool   `json:"disabled"`
	TokenHash string `json:"token_hash,omitempty"`
	Created   int64  `json:"created"`
}
type Node struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Region  string `json:"region"`
	Enabled bool   `json:"enabled"`
}
type Audit struct {
	At     int64  `json:"at"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Object string `json:"object"`
}
type Settings struct {
	MaxSessions int    `json:"max_sessions"`
	Banner      string `json:"banner"`
}
type catalogData struct {
	Users    map[string]User   `json:"users"`
	Nodes    map[string]Node   `json:"nodes"`
	Releases []update.Envelope `json:"releases"`
	Audit    []Audit           `json:"audit"`
	Settings Settings          `json:"settings"`
}
type catalog struct {
	mu      sync.Mutex
	data    catalogData
	backend StateBackend
}
type loginSession struct {
	User    string
	Expires time.Time
}

func newCatalog(b StateBackend) (*catalog, error) {
	c := &catalog{backend: b}
	if b != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		raw, e := b.Load(ctx)
		if e != nil {
			return nil, e
		}
		if len(raw) > 0 {
			if e = json.Unmarshal(raw, &c.data); e != nil {
				return nil, e
			}
		}
	}
	if c.data.Users == nil {
		c.data.Users = map[string]User{}
	}
	if c.data.Nodes == nil {
		c.data.Nodes = map[string]Node{}
	}
	if c.data.Settings.MaxSessions == 0 {
		c.data.Settings.MaxSessions = 2048
	}
	return c, nil
}
func (c *catalog) tx(actor, action, object string, fn func(*catalogData) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	old, _ := json.Marshal(c.data)
	if e := fn(&c.data); e != nil {
		_ = json.Unmarshal(old, &c.data)
		return e
	}
	c.data.Audit = append(c.data.Audit, Audit{time.Now().Unix(), actor, action, object})
	if len(c.data.Audit) > 2000 {
		c.data.Audit = append([]Audit(nil), c.data.Audit[len(c.data.Audit)-2000:]...)
	}
	if c.backend != nil {
		b, _ := json.Marshal(c.data)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e := c.backend.Save(ctx, b); e != nil {
			_ = json.Unmarshal(old, &c.data)
			return e
		}
	}
	return nil
}
func (c *catalog) maxSessions() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data.Settings.MaxSessions
}
func secretHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func validName(s string) bool {
	return len(s) > 0 && len([]rune(s)) <= 100 && strings.IndexFunc(s, unicode.IsControl) < 0
}
func (s *Server) tokenUser(token string) (string, string) {
	if sameSecret(token, s.cfg.AdminToken) {
		return "root", "admin"
	}
	hash := secretHash(token)
	s.adminState.mu.Lock()
	defer s.adminState.mu.Unlock()
	for _, u := range s.adminState.data.Users {
		if !u.Disabled && sameSecret(hash, u.TokenHash) {
			return u.ID, u.Role
		}
	}
	return "", ""
}
func (s *Server) userRole(id string) string {
	if id == "root" {
		return "admin"
	}
	s.adminState.mu.Lock()
	defer s.adminState.mu.Unlock()
	u, ok := s.adminState.data.Users[id]
	if !ok || u.Disabled {
		return ""
	}
	return u.Role
}
func (s *Server) adminIdentity(r *http.Request) (string, string) {
	if r.Header.Get("Authorization") != "" {
		return s.tokenUser(bearer(r))
	}
	cookie, e := r.Cookie("rd_admin")
	if e != nil {
		return "", ""
	}
	s.mu.Lock()
	ss, ok := s.loginSessions[secretHash(cookie.Value)]
	if !ok || time.Now().After(ss.Expires) {
		delete(s.loginSessions, secretHash(cookie.Value))
		s.mu.Unlock()
		return "", ""
	}
	s.mu.Unlock()
	return ss.User, s.userRole(ss.User)
}
func sameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		return e == nil && u.Scheme == "https" && u.Host == r.Host
	}
	return r.Header.Get("Authorization") != ""
}
func readAdmin(w http.ResponseWriter, r *http.Request, v any) bool {
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if e != nil || decode(b, v) != nil {
		fail(w, 400, "invalid management request")
		return false
	}
	return true
}
func (s *Server) manage(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" && r.Method != "DELETE" {
		fail(w, 405, "method not allowed")
		return
	}
	if r.Method != "GET" && !sameOrigin(r) {
		fail(w, 403, "same-origin management request required")
		return
	}
	if r.URL.Path == "/v1/admin/login" && r.Method == "POST" {
		var q struct {
			Token string `json:"token"`
		}
		if !readAdmin(w, r, &q) {
			return
		}
		actor, role := s.tokenUser(q.Token)
		if actor == "" {
			fail(w, 401, "invalid login token")
			return
		}
		token := protocol.RandomHex(32)
		s.mu.Lock()
		for k, v := range s.loginSessions {
			if time.Now().After(v.Expires) {
				delete(s.loginSessions, k)
			}
		}
		if len(s.loginSessions) >= 1024 {
			s.mu.Unlock()
			fail(w, 429, "login session limit")
			return
		}
		s.loginSessions[secretHash(token)] = loginSession{actor, time.Now().Add(8 * time.Hour)}
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "rd_admin", Value: token, Path: "/v1/admin/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: 8 * 3600})
		jsonResponse(w, 200, map[string]string{"user": actor, "role": role})
		return
	}
	actor, role := s.adminIdentity(r)
	if actor == "" || role == "" {
		fail(w, 401, "administrator authorization required")
		return
	}
	if r.URL.Path == "/v1/admin/logout" && r.Method == "POST" {
		if c, e := r.Cookie("rd_admin"); e == nil {
			s.mu.Lock()
			delete(s.loginSessions, secretHash(c.Value))
			s.mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: "rd_admin", Path: "/v1/admin/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		jsonResponse(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.Method != "GET" && role != "admin" && (role != "operator" || r.URL.Path != "/v1/admin/session-close") {
		fail(w, 403, "role does not permit this change")
		return
	}
	ok := func(e error) {
		if e != nil {
			fail(w, 409, e.Error())
		} else {
			jsonResponse(w, 200, map[string]bool{"ok": true})
		}
	}
	switch r.URL.Path {
	case "/v1/admin/me":
		if r.Method != "GET" {
			break
		}
		jsonResponse(w, 200, map[string]string{"user": actor, "role": role})
		return
	case "/v1/admin/overview", "/v1/admin/devices":
		if r.Method != "GET" {
			break
		}
		peers := []protocol.Peer{}
		online := 0
		for _, d := range s.store.List() {
			p, _ := s.peer(d.ID)
			peers = append(peers, p)
			if p.Online {
				online++
			}
		}
		if r.URL.Path == "/v1/admin/devices" {
			jsonResponse(w, 200, peers)
			return
		}
		s.mu.Lock()
		ss := []protocol.SessionStatus{}
		for _, v := range s.sessions {
			ss = append(ss, v.status)
		}
		s.mu.Unlock()
		sort.Slice(ss, func(i, j int) bool { return ss[i].ID < ss[j].ID })
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		backend := "json / process memory"
		if s.presence != nil {
			backend = "external registry / Redis presence; single signal process"
		}
		jsonResponse(w, 200, map[string]any{"devices": peers, "online": online, "sessions": ss, "version": "0.2.0-engineering", "backend": backend, "memory_bytes": mem.Alloc, "goroutines": runtime.NumGoroutine(), "limitations": []string{"Windows GPU performance requires hardware acceptance", "No multi-node signal routing"}})
		return
	case "/v1/admin/device":
		if r.Method != "POST" {
			break
		}
		var q struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Disabled bool   `json:"disabled"`
			Owner    string `json:"owner"`
		}
		if !readAdmin(w, r, &q) {
			return
		}
		d, exists := s.store.Get(q.ID)
		if !exists || !validName(q.Name) || len(q.Owner) > 100 {
			fail(w, 400, "invalid device update")
			return
		}
		d.Name = q.Name
		d.Disabled = q.Disabled
		d.Owner = q.Owner
		if e := s.store.Update(d); e != nil {
			ok(e)
			return
		}
		if d.Disabled {
			s.closeDeviceSessions(d.ID)
		}
		ok(s.adminState.tx(actor, "device.update", d.ID, func(*catalogData) error { return nil }))
		return
	case "/v1/admin/session-close":
		if r.Method != "POST" {
			break
		}
		var q struct {
			ID string `json:"id"`
		}
		if !readAdmin(w, r, &q) {
			return
		}
		s.mu.Lock()
		v, exists := s.sessions[q.ID]
		if exists {
			v.status.Closed = true
			for _, c := range v.conn {
				if c != nil {
					c.Close()
				}
			}
		}
		s.mu.Unlock()
		if !exists {
			fail(w, 404, "session not found")
			return
		}
		ok(s.adminState.tx(actor, "session.close", q.ID, func(*catalogData) error { return nil }))
		return
	case "/v1/admin/users":
		if role != "admin" {
			fail(w, 403, "admin role required")
			return
		}
		if r.Method == "GET" {
			s.adminState.mu.Lock()
			users := []User{}
			for _, u := range s.adminState.data.Users {
				u.TokenHash = ""
				users = append(users, u)
			}
			s.adminState.mu.Unlock()
			jsonResponse(w, 200, users)
			return
		}
		if r.Method == "POST" {
			var q struct {
				Name string `json:"name"`
				Role string `json:"role"`
			}
			if !readAdmin(w, r, &q) {
				return
			}
			if !validName(q.Name) || (q.Role != "admin" && q.Role != "operator" && q.Role != "viewer") {
				fail(w, 400, "invalid user name/role")
				return
			}
			token := protocol.RandomHex(32)
			u := User{protocol.RandomHex(16), q.Name, q.Role, false, secretHash(token), time.Now().Unix()}
			e := s.adminState.tx(actor, "user.create", u.ID, func(d *catalogData) error {
				if len(d.Users) >= 1000 {
					return errors.New("user capacity")
				}
				d.Users[u.ID] = u
				return nil
			})
			if e != nil {
				ok(e)
				return
			}
			u.TokenHash = ""
			jsonResponse(w, 201, map[string]any{"user": u, "token": token, "notice": "token is returned once; save it privately"})
			return
		}
		if r.Method == "DELETE" {
			id := r.URL.Query().Get("id")
			ok(s.adminState.tx(actor, "user.delete", id, func(d *catalogData) error {
				if _, exists := d.Users[id]; !exists {
					return errors.New("user not found")
				}
				delete(d.Users, id)
				return nil
			}))
			return
		}
	case "/v1/admin/nodes":
		if r.Method == "GET" {
			s.adminState.mu.Lock()
			nodes := []Node{}
			for _, n := range s.adminState.data.Nodes {
				nodes = append(nodes, n)
			}
			s.adminState.mu.Unlock()
			jsonResponse(w, 200, map[string]any{"nodes": nodes, "routing_active": false})
			return
		}
		if r.Method == "POST" {
			var n Node
			if !readAdmin(w, r, &n) {
				return
			}
			u, e := url.Parse(n.Address)
			if !validName(n.Name) || e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(n.Region) > 100 {
				fail(w, 400, "invalid node record")
				return
			}
			if n.ID == "" {
				n.ID = protocol.RandomHex(16)
			}
			ok(s.adminState.tx(actor, "node.save", n.ID, func(d *catalogData) error {
				if len(d.Nodes) >= 1000 {
					return errors.New("node capacity")
				}
				d.Nodes[n.ID] = n
				return nil
			}))
			return
		}
		if r.Method == "DELETE" {
			id := r.URL.Query().Get("id")
			ok(s.adminState.tx(actor, "node.delete", id, func(d *catalogData) error { delete(d.Nodes, id); return nil }))
			return
		}
	case "/v1/admin/releases":
		if r.Method == "GET" {
			s.adminState.mu.Lock()
			releases := append([]update.Envelope{}, s.adminState.data.Releases...)
			s.adminState.mu.Unlock()
			jsonResponse(w, 200, releases)
			return
		}
		if r.Method == "POST" {
			var env update.Envelope
			if !readAdmin(w, r, &env) {
				return
			}
			key, e := update.ParsePublic(s.cfg.UpdatePublicKey)
			if e != nil {
				fail(w, 409, "configure a pinned update_public_key before publishing")
				return
			}
			b, _ := json.Marshal(env)
			m, e := update.Verify(b, key, 0, env.Manifest.Platform, time.Now())
			if e != nil {
				ok(e)
				return
			}
			ok(s.adminState.tx(actor, "release.publish", m.Version, func(d *catalogData) error {
				for _, v := range d.Releases {
					if v.Manifest.Platform == m.Platform && v.Manifest.Sequence >= m.Sequence {
						return errors.New("release sequence must increase")
					}
				}
				d.Releases = append(d.Releases, env)
				if len(d.Releases) > 100 {
					d.Releases = d.Releases[len(d.Releases)-100:]
				}
				return nil
			}))
			return
		}
	case "/v1/admin/audit":
		if r.Method != "GET" {
			break
		}
		s.adminState.mu.Lock()
		events := append([]Audit{}, s.adminState.data.Audit...)
		s.adminState.mu.Unlock()
		jsonResponse(w, 200, events)
		return
	case "/v1/admin/config":
		if r.Method == "GET" {
			s.adminState.mu.Lock()
			settings := s.adminState.data.Settings
			s.adminState.mu.Unlock()
			jsonResponse(w, 200, map[string]any{"settings": settings, "listen": s.cfg.Listen, "stun": s.cfg.STUN, "update_signing_configured": s.cfg.UpdatePublicKey != "", "device_private_keys_on_server": false})
			return
		}
		if r.Method == "POST" {
			var settings Settings
			if !readAdmin(w, r, &settings) {
				return
			}
			if settings.MaxSessions < 1 || settings.MaxSessions > 10000 || len(settings.Banner) > 500 {
				fail(w, 400, "invalid settings bounds")
				return
			}
			ok(s.adminState.tx(actor, "config.update", "settings", func(d *catalogData) error { d.Settings = settings; return nil }))
			return
		}
	}
	fail(w, 404, "unknown management endpoint or method")
}
func (s *Server) closeDeviceSessions(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.sessions {
		if v.status.From == id || v.status.To == id {
			v.status.Closed = true
			for _, c := range v.conn {
				if c != nil {
					c.Close()
				}
			}
		}
	}
}
func (s *Server) latestRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		fail(w, 405, "GET required")
		return
	}
	platform := r.URL.Query().Get("platform")
	s.adminState.mu.Lock()
	defer s.adminState.mu.Unlock()
	var latest *update.Envelope
	for i := range s.adminState.data.Releases {
		e := &s.adminState.data.Releases[i]
		if e.Manifest.Platform == platform && e.Manifest.ExpiresAt > time.Now().Unix() && (latest == nil || e.Manifest.Sequence > latest.Manifest.Sequence) {
			latest = e
		}
	}
	if latest == nil {
		fail(w, 404, "no current signed release for platform")
		return
	}
	jsonResponse(w, 200, latest)
}
