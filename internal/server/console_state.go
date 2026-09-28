package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"remotedesk.local/remotedesk/internal/identity"
	"remotedesk.local/remotedesk/internal/monitor"
)

type DeviceMeta struct {
	Alias    string `json:"alias"`
	Group    string `json:"group"`
	Notes    string `json:"notes"`
	Disabled bool   `json:"disabled"`
}
type SiteSettings struct {
	NodeName         string `json:"node_name"`
	Notice           string `json:"notice"`
	RegistrationOpen bool   `json:"registration_open"`
}
type AuditEvent struct {
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Result string    `json:"result"`
}
type ReleaseInfo struct {
	Version     string    `json:"version"`
	URL         string    `json:"url"`
	SHA256      string    `json:"sha256"`
	Notes       string    `json:"notes"`
	PublishedAt time.Time `json:"published_at"`
}
type adminData struct {
	Settings SiteSettings          `json:"settings"`
	Devices  map[string]DeviceMeta `json:"devices"`
	Audit    []AuditEvent          `json:"audit"`
	Releases []ReleaseInfo         `json:"releases"`
}
type adminSession struct {
	Expires time.Time
	CSRF    string
}
type loginWindow struct {
	Until time.Time
	Count int
}
type administration struct {
	mu       sync.Mutex
	path     string
	data     adminData
	sessions map[string]adminSession
	failures map[string]loginWindow
}

func newAdministration(storePath string) (*administration, error) {
	a := &administration{sessions: map[string]adminSession{}, failures: map[string]loginWindow{}}
	a.data = adminData{Settings: SiteSettings{NodeName: monitor.Hostname(), RegistrationOpen: true}, Devices: map[string]DeviceMeta{}, Audit: []AuditEvent{}, Releases: []ReleaseInfo{}}
	if storePath != "" {
		a.path = filepath.Join(filepath.Dir(storePath), "admin-state.json")
		if e := identity.ReadJSON(a.path, &a.data); e != nil && !os.IsNotExist(e) {
			return nil, e
		}
	}
	if a.data.Devices == nil {
		a.data.Devices = map[string]DeviceMeta{}
	}
	if a.data.Audit == nil {
		a.data.Audit = []AuditEvent{}
	}
	if a.data.Releases == nil {
		a.data.Releases = []ReleaseInfo{}
	}
	if len(a.data.Devices) > 10000 || len(a.data.Audit) > 1000 || len(a.data.Releases) > 100 {
		return nil, errors.New("admin state exceeds limits")
	}
	if !safeText(a.data.Settings.NodeName, 100) || !safeText(a.data.Settings.Notice, 2000) {
		return nil, errors.New("invalid admin settings")
	}
	return a, nil
}
func safeText(v string, max int) bool {
	return len([]rune(v)) <= max && strings.IndexFunc(v, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) < 0
}
func cloneAdmin(d adminData) adminData {
	n := d
	n.Devices = make(map[string]DeviceMeta, len(d.Devices))
	for k, v := range d.Devices {
		n.Devices[k] = v
	}
	n.Audit = append([]AuditEvent{}, d.Audit...)
	n.Releases = append([]ReleaseInfo{}, d.Releases...)
	return n
}
func (a *administration) snapshot() adminData {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneAdmin(a.data)
}

// Persist before changing visible state. A failed disk write must not appear saved in the UI.
func (a *administration) mutate(action, target string, fn func(*adminData) error) error {
	return a.mutateAs("admin", action, target, fn)
}
func (a *administration) mutateAs(actor, action, target string, fn func(*adminData) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := cloneAdmin(a.data)
	if e := fn(&next); e != nil {
		return e
	}
	next.Audit = append(next.Audit, AuditEvent{time.Now().UTC(), actor, action, target, "success"})
	if len(next.Audit) > 1000 {
		next.Audit = next.Audit[len(next.Audit)-1000:]
	}
	if a.path != "" {
		if e := identity.WriteJSON(a.path, next); e != nil {
			return e
		}
	}
	a.data = next
	return nil
}
func (a *administration) disabled(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.data.Devices[id].Disabled
}
func (a *administration) registrationOpen() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.data.Settings.RegistrationOpen
}
