package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"remotedesk.local/remotedesk/internal/protocol"
	"remotedesk.local/remotedesk/internal/store"
)

func adminFixture(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	cfg := Config{AdminToken: strings.Repeat("a", 64), EnrollmentToken: strings.Repeat("e", 64), Listen: "127.0.0.1:8443", STUN: "127.0.0.1:3478", Store: filepath.Join(t.TempDir(), "devices.json")}
	st, e := store.Open(cfg.Store)
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(cfg, st)
	if e != nil {
		t.Fatal(e)
	}
	ts := httptest.NewTLSServer(s)
	t.Cleanup(func() { ts.Close(); s.Close() })
	return s, ts
}
func adminRequest(t *testing.T, ts *httptest.Server, method, path string, body any, cookie *http.Cookie, csrf string, bearer bool) *http.Response {
	t.Helper()
	var data []byte
	if body != nil {
		var e error
		data, e = json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
	}
	r, e := http.NewRequest(method, ts.URL+path, bytes.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-RD-CSRF", csrf)
	}
	if bearer {
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	}
	resp, e := ts.Client().Do(r)
	if e != nil {
		t.Fatal(e)
	}
	return resp
}
func loginFixture(t *testing.T, ts *httptest.Server) (*http.Cookie, string) {
	t.Helper()
	r := adminRequest(t, ts, "POST", "/v1/admin/login", map[string]string{"username": "admin", "password": strings.Repeat("a", 64)}, nil, "", false)
	defer r.Body.Close()
	if r.StatusCode != 200 {
		b, _ := io.ReadAll(r.Body)
		t.Fatal(r.StatusCode, string(b))
	}
	var v struct {
		CSRF string `json:"csrf"`
	}
	if e := json.NewDecoder(r.Body).Decode(&v); e != nil {
		t.Fatal(e)
	}
	for _, cookie := range r.Cookies() {
		if cookie.Name == adminCookie {
			return cookie, v.CSRF
		}
	}
	t.Fatal("no admin cookie")
	return nil, ""
}
func expectStatus(t *testing.T, r *http.Response, want int) {
	t.Helper()
	defer r.Body.Close()
	if r.StatusCode != want {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("status %d, want %d: %s", r.StatusCode, want, b)
	}
}
func TestAdminRequiresAuthentication(t *testing.T) {
	_, ts := adminFixture(t)
	for _, path := range []string{"overview", "metrics", "audit", "settings", "releases", "me"} {
		expectStatus(t, adminRequest(t, ts, "GET", "/v1/admin/"+path, nil, nil, "", false), 401)
	}
}
func TestAdminCookieAndCSRF(t *testing.T) {
	s, ts := adminFixture(t)
	cookie, csrf := loginFixture(t, ts)
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || len(csrf) != 64 {
		t.Fatal("unsafe cookie or CSRF")
	}
	if _, ok := s.loginSessions[cookie.Value]; ok {
		t.Fatal("plaintext session stored")
	}
	expectStatus(t, adminRequest(t, ts, "GET", "/v1/admin/overview", nil, cookie, "", false), 200)
	settings := SiteSettings{"New server", "hello", false}
	expectStatus(t, adminRequest(t, ts, "PUT", "/v1/admin/settings", settings, cookie, "", false), 403)
	expectStatus(t, adminRequest(t, ts, "PUT", "/v1/admin/settings", settings, cookie, csrf, false), 200)
	if s.console.registrationOpen() {
		t.Fatal("setting was not saved")
	}
}
func TestAdminCrossOriginAndNoTLSLoginRejected(t *testing.T) {
	s, ts := adminFixture(t)
	cookie, csrf := loginFixture(t, ts)
	for _, path := range []string{"login", "logout"} {
		r, _ := http.NewRequest("POST", ts.URL+"/v1/admin/"+path, strings.NewReader(`{}`))
		r.Header.Set("Origin", "https://evil.invalid")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-RD-CSRF", csrf)
		r.AddCookie(cookie)
		response, e := ts.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		expectStatus(t, response, 403)
	}
	r := httptest.NewRequest("POST", "http://localhost/v1/admin/login", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestAdminLogoutInvalidatesSession(t *testing.T) {
	_, ts := adminFixture(t)
	cookie, csrf := loginFixture(t, ts)
	expectStatus(t, adminRequest(t, ts, "POST", "/v1/admin/logout", map[string]bool{}, cookie, csrf, false), 200)
	expectStatus(t, adminRequest(t, ts, "GET", "/v1/admin/me", nil, cookie, "", false), 401)
}
func TestAdminLoginRateLimit(t *testing.T) {
	_, ts := adminFixture(t)
	for i := 0; i < 5; i++ {
		expectStatus(t, adminRequest(t, ts, "POST", "/v1/admin/login", map[string]string{"username": "admin", "password": "wrong"}, nil, "", false), 401)
	}
	expectStatus(t, adminRequest(t, ts, "POST", "/v1/admin/login", map[string]string{"username": "admin", "password": strings.Repeat("a", 64)}, nil, "", false), 429)
}
func TestAdminStatePersistsAndContainsNoCredentials(t *testing.T) {
	s, ts := adminFixture(t)
	cookie, csrf := loginFixture(t, ts)
	expectStatus(t, adminRequest(t, ts, "PUT", "/v1/admin/settings", SiteSettings{"Singapore node", "维护窗口", true}, cookie, csrf, false), 200)
	again, e := newAdministration(s.cfg.Store)
	if e != nil {
		t.Fatal(e)
	}
	if again.snapshot().Settings.NodeName != "Singapore node" {
		t.Fatal("state lost")
	}
	r := adminRequest(t, ts, "GET", "/v1/admin/overview", nil, cookie, "", false)
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	for _, secret := range []string{s.cfg.AdminToken, s.cfg.EnrollmentToken, cookie.Value, csrf} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("secret leaked in overview")
		}
	}
	var data map[string]any
	if e := json.Unmarshal(b, &data); e != nil {
		t.Fatal(e)
	}
	if data["active_direct_p2p"] != nil {
		t.Fatal("fabricated direct P2P measurement")
	}
	node := data["server"].(map[string]any)
	if node["status"] != "online" || node["heartbeat_at"] == nil {
		t.Fatal("missing real heartbeat")
	}
}
func TestAdminWriteFailureDoesNotChangeMemory(t *testing.T) {
	s, _ := adminFixture(t)
	before := s.console.snapshot().Settings
	s.console.path = t.TempDir()
	e := s.console.mutate("test", "server", func(d *adminData) error { d.Settings.NodeName = "must not stick"; return nil })
	if e == nil || s.console.snapshot().Settings != before {
		t.Fatal("failed save changed live data")
	}
}
func TestAdminDeviceMetadataAndBlock(t *testing.T) {
	s, ts := adminFixture(t)
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	id := protocol.DeviceID(public)
	_, e := s.store.Register(protocol.Device{ID: id, Name: "Test PC", PublicKey: protocol.PublicString(public), RegisteredAt: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	s.online[id] = live{seen: time.Now()}
	s.mu.Unlock()
	cookie, csrf := loginFixture(t, ts)
	meta := DeviceMeta{"Office PC", "Work", "owner test", true}
	expectStatus(t, adminRequest(t, ts, "PATCH", "/v1/admin/devices/"+id, meta, cookie, csrf, false), 200)
	if !s.console.disabled(id) {
		t.Fatal("not blocked")
	}
	if peer, _ := s.peer(id); peer.Online {
		t.Fatal("blocked peer is discoverable")
	}
	r, _ := http.NewRequest("POST", ts.URL+"/v1/heartbeat", strings.NewReader(`{}`))
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := protocol.RandomHex(16)
	key := protocol.PublicString(public)
	r.Header.Set("X-RD-Key", key)
	r.Header.Set("X-RD-Time", stamp)
	r.Header.Set("X-RD-Nonce", nonce)
	r.Header.Set("X-RD-Signature", protocol.Sign(private, protocol.CanonicalRequest("POST", "/v1/heartbeat", stamp, nonce, key, []byte(`{}`))))
	response, e := ts.Client().Do(r)
	if e != nil {
		t.Fatal(e)
	}
	expectStatus(t, response, 403)
}
func TestUnsignedReleaseRemainsRejected(t *testing.T) {
	_, ts := adminFixture(t)
	cookie, csrf := loginFixture(t, ts)
	r := adminRequest(t, ts, "POST", "/v1/admin/releases", map[string]any{"version": "0.3.0", "url": "https://example.test/RemoteDeskSetup.exe"}, cookie, csrf, false)
	expectStatus(t, r, 400)
}
func TestBearerCompatibilityAndStaticRedirect(t *testing.T) {
	s, ts := adminFixture(t)
	expectStatus(t, adminRequest(t, ts, "GET", "/v1/admin/overview", nil, nil, "", true), 200)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/", nil))
	if w.Code != http.StatusTemporaryRedirect || w.Header().Get("Location") != "/admin/" {
		t.Fatal("missing admin redirect")
	}
}

func TestCatalogFailedSaveRollsBackNewMapKey(t *testing.T) {
	c, _ := newCatalog(nil)
	c.backend = FileState{Path: t.TempDir()}
	if e := c.tx("root", "test", "test", func(d *catalogData) error { d.Users["new"] = User{ID: "new"}; return nil }); e == nil {
		t.Fatal("expected write failure")
	}
	if _, exists := c.data.Users["new"]; exists {
		t.Fatal("failed save left a new user in memory")
	}
}
func TestRevokedCookieCannotUseNewDashboard(t *testing.T) {
	s, ts := adminFixture(t)
	token := strings.Repeat("u", 64)
	s.adminState.data.Users["viewer"] = User{ID: "viewer", Role: "viewer", TokenHash: secretHash(token)}
	r := adminRequest(t, ts, "POST", "/v1/admin/login", map[string]string{"token": token}, nil, "", false)
	var data struct {
		CSRF string `json:"csrf"`
	}
	json.NewDecoder(r.Body).Decode(&data)
	r.Body.Close()
	cookies := r.Cookies()
	if len(cookies) != 1 {
		t.Fatal("login failed")
	}
	expectStatus(t, adminRequest(t, ts, "GET", "/v1/admin/overview", nil, cookies[0], "", false), 200)
	expectStatus(t, adminRequest(t, ts, "PUT", "/v1/admin/settings", SiteSettings{"test", "", true}, cookies[0], data.CSRF, false), 403)
	s.adminState.mu.Lock()
	delete(s.adminState.data.Users, "viewer")
	s.adminState.mu.Unlock()
	expectStatus(t, adminRequest(t, ts, "GET", "/v1/admin/metrics", nil, cookies[0], "", false), 401)
}
