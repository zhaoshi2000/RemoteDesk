package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"remotedesk.local/remotedesk/internal/monitor"
	"remotedesk.local/remotedesk/internal/protocol"
	"remotedesk.local/remotedesk/internal/telemetry"
	"strconv"
	"testing"
	"time"
)

func telemetryFixture(t *testing.T, s *Server) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	id := protocol.DeviceID(pub)
	_, e = s.store.Register(protocol.Device{ID: id, Name: "Test hardware reporter", PublicKey: protocol.PublicString(pub), RegisteredAt: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	return priv, id
}
func sampleReport() telemetry.Report {
	now := time.Now().UTC()
	return telemetry.Report{AgentVersion: telemetry.AgentVersion, CollectedAt: now, System: telemetry.System{OS: "test", Arch: "x64", CPUCores: 4, HardwareProbe: "partial"}, Hosting: telemetry.Hosting{Mode: "interactive"}, Sample: monitor.Sample{At: now}}
}
func signedReport(t *testing.T, ts *httptest.Server, key ed25519.PrivateKey, body any) *http.Request {
	t.Helper()
	data, e := json.Marshal(body)
	if e != nil {
		t.Fatal(e)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/v1/telemetry", bytes.NewReader(data))
	pub := protocol.PublicString(key.Public().(ed25519.PublicKey))
	stamp, nonce := strconv.FormatInt(time.Now().Unix(), 10), protocol.RandomHex(16)
	req.Header.Set("X-RD-Key", pub)
	req.Header.Set("X-RD-Time", stamp)
	req.Header.Set("X-RD-Nonce", nonce)
	req.Header.Set("X-RD-Signature", protocol.Sign(key, protocol.CanonicalRequest("POST", "/v1/telemetry", stamp, nonce, pub, data)))
	req.Header.Set("Content-Type", "application/json")
	return req
}
func sendReport(t *testing.T, ts *httptest.Server, r *http.Request) *http.Response {
	t.Helper()
	res, e := ts.Client().Do(r)
	if e != nil {
		t.Fatal(e)
	}
	return res
}
func TestTelemetryAuthenticatedAndAdminScoped(t *testing.T) {
	s, ts := adminFixture(t)
	key, id := telemetryFixture(t, s)
	expectStatus(t, sendReport(t, ts, signedReport(t, ts, key, sampleReport())), 200)
	data := s.telemetrySnapshot(id, true)
	if data == nil || data.Report.System.CPUCores != 4 || data.ObservedIP != "127.0.0.1" || len(data.History) != 1 {
		t.Fatal(data)
	}
	expectStatus(t, adminRequest(t, ts, "GET", "/v1/admin/devices/"+id, nil, nil, "", false), 401)
	cookie, _ := loginFixture(t, ts)
	res := adminRequest(t, ts, "GET", "/v1/admin/devices/"+id, nil, cookie, "", false)
	defer res.Body.Close()
	var detail AdminDevice
	if e := json.NewDecoder(res.Body).Decode(&detail); e != nil {
		t.Fatal(e)
	}
	if detail.Telemetry == nil || detail.Fingerprint == "" || detail.Device.ID != id {
		t.Fatal("incomplete detail")
	}
	expectStatus(t, sendReport(t, ts, signedReport(t, ts, key, map[string]any{"device_id": "other device"})), 400)
	// Device-authenticated status cannot be read as an administrative endpoint.
	req := signedReport(t, ts, key, sampleReport())
	req.Method = "GET"
	req.URL.Path = "/v1/admin/devices/" + id
	expectStatus(t, sendReport(t, ts, req), 401)
}
func TestTelemetryReplayAndDisabledReporter(t *testing.T) {
	s, ts := adminFixture(t)
	key, id := telemetryFixture(t, s)
	req := signedReport(t, ts, key, sampleReport())
	payload, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(payload))
	expectStatus(t, sendReport(t, ts, req), 200)
	again := req.Clone(req.Context())
	again.Body = io.NopCloser(bytes.NewReader(payload))
	expectStatus(t, sendReport(t, ts, again), 409)
	cookie, csrf := loginFixture(t, ts)
	expectStatus(t, adminRequest(t, ts, "PATCH", "/v1/admin/devices/"+id, DeviceMeta{Disabled: true}, cookie, csrf, false), 200)
	expectStatus(t, sendReport(t, ts, signedReport(t, ts, key, sampleReport())), 403)
}
func TestTelemetryRateCapacityAndHistory(t *testing.T) {
	s, ts := adminFixture(t)
	key, id := telemetryFixture(t, s)
	expectStatus(t, sendReport(t, ts, signedReport(t, ts, key, sampleReport())), 200)
	expectStatus(t, sendReport(t, ts, signedReport(t, ts, key, sampleReport())), 429)
	for i := 0; i < telemetryHistory+5; i++ {
		s.mu.Lock()
		old := s.telemetry[id]
		old.ReceivedAt = time.Now().Add(-4 * time.Second)
		s.telemetry[id] = old
		s.mu.Unlock()
		expectStatus(t, sendReport(t, ts, signedReport(t, ts, key, sampleReport())), 200)
	}
	if len(s.telemetrySnapshot(id, true).History) != telemetryHistory {
		t.Fatal("unbounded history")
	}
	d := s.telemetrySnapshot(id, true)
	d.History[0].Goroutines = 999
	if s.telemetrySnapshot(id, true).History[0].Goroutines == 999 {
		t.Fatal("history alias")
	}
	s.mu.Lock()
	for i := 0; i < maxTelemetryDevices; i++ {
		s.telemetry[strconv.Itoa(i)] = DeviceTelemetry{}
	}
	s.mu.Unlock()
	key2, _ := telemetryFixture(t, s)
	expectStatus(t, sendReport(t, ts, signedReport(t, ts, key2, sampleReport())), 429)
}
