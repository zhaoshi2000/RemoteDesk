package server

import (
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"remotedesk.local/remotedesk/internal/monitor"
	"remotedesk.local/remotedesk/internal/protocol"
)

const ServerVersion = "0.3.0"

type ServiceStatus struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Listen string `json:"listen"`
	Detail string `json:"detail"`
}
type AdminDevice struct {
	protocol.Peer
	Meta DeviceMeta `json:"meta"`
}

func consolePath(p string) bool {
	return p == "/v1/admin/settings" || p == "/v1/admin/metrics" || p == "/v1/admin/console-audit" || strings.HasPrefix(p, "/v1/admin/devices/") || strings.HasPrefix(p, "/v1/admin/sessions/")
}
func (s *Server) consoleAPI(w http.ResponseWriter, r *http.Request, actor string) {
	path := r.URL.Path
	switch {
	case path == "/v1/admin/metrics" && r.Method == "GET":
		jsonResponse(w, 200, map[string]any{"samples": s.monitor.History(), "interval_seconds": 5, "retention_samples": 720, "scope": "host counters; direct P2P bytes are not visible to the server"})
		return
	case path == "/v1/admin/console-audit" && r.Method == "GET":
		data := s.console.snapshot()
		sort.SliceStable(data.Audit, func(i, j int) bool { return data.Audit[i].At.After(data.Audit[j].At) })
		jsonResponse(w, 200, data.Audit)
		return
	case path == "/v1/admin/settings" && r.Method == "GET":
		jsonResponse(w, 200, s.console.snapshot().Settings)
		return
	case path == "/v1/admin/settings" && r.Method == "PUT":
		var req SiteSettings
		if !adminJSON(w, r, &req) {
			return
		}
		req.NodeName = strings.TrimSpace(req.NodeName)
		if req.NodeName == "" || !safeText(req.NodeName, 100) || !safeText(req.Notice, 2000) {
			fail(w, 400, "invalid settings")
			return
		}
		s.adminMutation(w, actor, "settings.update", "server", func(d *adminData) error { d.Settings = req; return nil })
		return
	case strings.HasPrefix(path, "/v1/admin/devices/") && r.Method == "PATCH":
		id := strings.TrimPrefix(path, "/v1/admin/devices/")
		if _, exists := s.store.Get(id); !exists {
			fail(w, 404, "unknown device")
			return
		}
		var req DeviceMeta
		if !adminJSON(w, r, &req) {
			return
		}
		if !safeText(req.Alias, 100) || !safeText(req.Group, 80) || !safeText(req.Notes, 2000) {
			fail(w, 400, "invalid device metadata")
			return
		}
		if e := s.console.mutateAs(actor, "device.update", id, func(d *adminData) error { d.Devices[id] = req; return nil }); e != nil {
			fail(w, 500, "could not persist device metadata")
			return
		}
		if req.Disabled {
			s.blockDevice(id)
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
		return
	case strings.HasPrefix(path, "/v1/admin/sessions/") && strings.HasSuffix(path, "/close") && r.Method == "POST":
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/v1/admin/sessions/"), "/close")
		s.mu.Lock()
		v, exists := s.sessions[id]
		paired := exists && v.status.Kind == "ssh" && v.status.Paired && !v.status.Closed
		s.mu.Unlock()
		if !paired {
			fail(w, 409, "only an active server-relayed SSH connection can be terminated here")
			return
		}
		if e := s.console.mutateAs(actor, "relay.close", id, func(*adminData) error { return nil }); e != nil {
			fail(w, 500, "could not persist audit")
			return
		}
		s.mu.Lock()
		if v = s.sessions[id]; v != nil {
			v.status.Closed = true
			for _, c := range v.conn {
				if c != nil {
					c.Close()
				}
			}
		}
		s.mu.Unlock()
		jsonResponse(w, 200, map[string]bool{"ok": true})
		return
	default:
		fail(w, 404, "unknown admin endpoint or method")
	}
}
func (s *Server) adminMutation(w http.ResponseWriter, actor, action, target string, fn func(*adminData) error) {
	if e := s.console.mutateAs(actor, action, target, fn); e != nil {
		fail(w, 500, "administrative change was not saved")
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (s *Server) blockDevice(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.online, id)
	delete(s.events, id)
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
	// This blocks coordinator access. It cannot revoke independent, already-established
	// direct peer connections; the device's local trust list remains the authority.
}
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	data := s.console.snapshot()
	devices := []AdminDevice{}
	legacy := []protocol.Peer{}
	online, blocked := 0, 0
	for _, d := range s.store.List() {
		p, _ := s.peer(d.ID)
		meta := data.Devices[d.ID]
		meta.Disabled = meta.Disabled || d.Disabled
		if meta.Disabled {
			blocked++
			p.Online = false
		}
		if p.Online {
			online++
		}
		if p.LastSeen < 0 {
			p.LastSeen = 0
		}
		devices = append(devices, AdminDevice{p, meta})
		legacy = append(legacy, p)
	}
	s.mu.Lock()
	ss := []protocol.SessionStatus{}
	activeSSH := 0
	for _, v := range s.sessions {
		ss = append(ss, v.status)
		if v.status.Kind == "ssh" && v.status.Paired && !v.status.Closed {
			activeSSH++
		}
	}
	components := make([]ServiceStatus, 0, len(s.components))
	for _, v := range s.components {
		components = append(components, v)
	}
	s.mu.Unlock()
	sort.Slice(components, func(i, j int) bool { return components[i].Name < components[j].Name })
	sort.Slice(ss, func(i, j int) bool { return ss[i].ID < ss[j].ID })
	sample := s.monitor.Latest()
	status := "online"
	if s.ctx.Err() != nil {
		status = "stopping"
	} else if now.Sub(sample.At) > 15*time.Second {
		status = "degraded"
	}
	relay := s.relayStats()
	storage := "atomic JSON (single node)"
	if s.presence != nil {
		storage = "external registry / Redis; single signal process"
	}

	jsonResponse(w, 200, map[string]any{
		"server_time": now, "version": ServerVersion, "server": map[string]any{"name": data.Settings.NodeName, "hostname": monitor.Hostname(), "status": status, "heartbeat_at": sample.At, "started_at": s.started, "uptime_seconds": int64(now.Sub(s.started).Seconds()), "os": runtime.GOOS, "arch": runtime.GOARCH, "go_version": runtime.Version(), "cpu_cores": runtime.NumCPU(), "components": components, "sample": sample, "total_requests": s.requests.Load(), "store": storage, "external_reachability": "not externally probed"},
		"devices": legacy, "managed_devices": devices, "online": online, "disabled": blocked, "sessions": ss, "active_ssh_relay": activeSSH, "media_relay": relay, "active_direct_p2p": nil, "notice": data.Settings.Notice,
		"limitations": []string{"Direct P2P session count/traffic is not reported by this Agent version", "Node metrics show this running server, not an external connectivity guarantee", "Device blocking affects the coordinator, not existing direct peer authorization", "GPU remote desktop performance remains unverified"},
	})
}
func (s *Server) SetComponent(name, state, listen, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.components[name] = ServiceStatus{name, state, listen, detail}
}
func (s *Server) collectMetrics() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.monitor.Collect()
		}
	}
}
