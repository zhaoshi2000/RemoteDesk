package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"remotedesk.local/remotedesk/internal/store"
	"strings"
	"testing"
)

func adminServer(t *testing.T) *Server {
	t.Helper()
	st, _ := store.Open("")
	s, e := New(Config{AdminToken: strings.Repeat("a", 64), EnrollmentToken: strings.Repeat("b", 64)}, st)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}
func req(s *Server, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://coordinator.test"+path, bytes.NewBufferString(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestAdminAuthorization(t *testing.T) {
	s := adminServer(t)
	if w := req(s, "GET", "/v1/admin/overview", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := req(s, "POST", "/v1/admin/users", s.cfg.AdminToken, `{"name":"reader","role":"viewer"}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var body struct {
		Token string `json:"token"`
		User  User   `json:"user"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body.Token == "" || body.User.TokenHash != "" {
		t.Fatal("user token exposure")
	}
	if w = req(s, "GET", "/v1/admin/overview", body.Token, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = req(s, "POST", "/v1/admin/config", body.Token, `{"max_sessions":2,"banner":"x"}`); w.Code != 403 {
		t.Fatal("viewer wrote settings")
	}
	if w = req(s, "DELETE", "/v1/admin/users?id="+body.User.ID, s.cfg.AdminToken, ""); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w = req(s, "GET", "/v1/admin/overview", body.Token, ""); w.Code != 401 {
		t.Fatal("revoked user retained access")
	}
}
func TestAdminCSRF(t *testing.T) {
	s := adminServer(t)
	r := httptest.NewRequest("POST", "https://coordinator.test/v1/admin/login", strings.NewReader(`{"token":"`+s.cfg.AdminToken+`"}`))
	r.Header.Set("Origin", "https://evil.test")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("cross-origin login accepted")
	}
	r = httptest.NewRequest("POST", "https://coordinator.test/v1/admin/login", strings.NewReader(`{"token":"`+s.cfg.AdminToken+`"}`))
	r.Header.Set("Origin", "https://coordinator.test")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe session cookie")
	}
}
