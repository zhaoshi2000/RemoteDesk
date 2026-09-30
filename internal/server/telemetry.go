package server

import (
	"net"
	"net/http"
	"remotedesk.local/remotedesk/internal/monitor"
	"remotedesk.local/remotedesk/internal/telemetry"
	"time"
)

const maxTelemetryDevices = 2048
const telemetryHistory = 40

// Reports are immutable after insertion. Deep copies protect mutable history storage.
type DeviceTelemetry struct {
	Report     telemetry.Report `json:"report"`
	ReceivedAt time.Time        `json:"received_at"`
	ObservedIP string           `json:"observed_ip"`
	History    []monitor.Sample `json:"history,omitempty"`
}

func (s *Server) acceptTelemetry(w http.ResponseWriter, r *http.Request, id string, body []byte) {
	var report telemetry.Report
	now := time.Now().UTC()
	if len(body) > 24*1024 || decode(body, &report) != nil || report.Validate(now) != nil {
		fail(w, 400, "invalid device telemetry")
		return
	}
	ip, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		ip = ""
	}
	s.mu.Lock()
	previous, exists := s.telemetry[id]
	if exists && now.Sub(previous.ReceivedAt) < 3*time.Second {
		s.mu.Unlock()
		fail(w, 429, "telemetry interval too short")
		return
	}
	if !exists && len(s.telemetry) >= maxTelemetryDevices {
		s.mu.Unlock()
		fail(w, 429, "telemetry capacity reached")
		return
	}
	history := append([]monitor.Sample(nil), previous.History...)
	if len(history) >= telemetryHistory {
		history = history[len(history)-telemetryHistory+1:]
	}
	history = append(history, report.Sample)
	s.telemetry[id] = DeviceTelemetry{report, now, ip, history}
	s.mu.Unlock()
	jsonResponse(w, 200, map[string]any{"ok": true, "received_at": now})
}
func (s *Server) telemetrySnapshot(id string, history bool) *DeviceTelemetry {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.telemetry[id]
	if !ok {
		return nil
	}
	if history {
		d.History = append([]monitor.Sample(nil), d.History...)
	} else {
		d.History = nil
	}
	return &d
}
