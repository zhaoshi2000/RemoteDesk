// Package telemetry defines explicitly scoped, signed device status reports.
// It never collects screen contents, clipboard data, credentials or file lists.
package telemetry

import (
	"context"
	"errors"
	"math"
	"net"
	"os"
	"remotedesk.local/remotedesk/internal/monitor"
	"runtime"
	"strings"
	"time"
	"unicode"
)

const AgentVersion = "0.4.0-preview"

type GPU struct {
	Name   string `json:"name"`
	Driver string `json:"driver"`
}
type Display struct {
	Name    string `json:"name"`
	Width   uint32 `json:"width"`
	Height  uint32 `json:"height"`
	Refresh uint32 `json:"refresh_hz"`
}
type System struct {
	Hostname      string    `json:"hostname"`
	OS            string    `json:"os"`
	OSVersion     string    `json:"os_version"`
	Arch          string    `json:"arch"`
	CPUModel      string    `json:"cpu_model"`
	CPUCores      int       `json:"cpu_cores"`
	GPUs          []GPU     `json:"gpus"`
	Displays      []Display `json:"displays"`
	HardwareProbe string    `json:"hardware_probe"`
}
type Hosting struct {
	Mode               string `json:"mode"`
	DesktopEnabled     bool   `json:"desktop_enabled"`
	FileSharingEnabled bool   `json:"file_sharing_enabled"`
	MediaWorkerPresent bool   `json:"media_worker_present"`
	SSHListening       bool   `json:"ssh_listening"`
}
type Report struct {
	AgentVersion  string         `json:"agent_version"`
	CollectedAt   time.Time      `json:"collected_at"`
	UptimeSeconds int64          `json:"uptime_seconds"`
	System        System         `json:"system"`
	LocalIPs      []string       `json:"local_ips"`
	Hosting       Hosting        `json:"hosting"`
	Sample        monitor.Sample `json:"sample"`
}

func Inspect(ctx context.Context) System {
	host, _ := os.Hostname()
	s := System{Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH, CPUCores: runtime.NumCPU(), GPUs: []GPU{}, Displays: []Display{}, HardwareProbe: "unavailable"}
	platformInspect(ctx, &s)
	return s
}
func LocalIPs() []string {
	out := []string{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		ip, _, err := net.ParseCIDR(a.String())
		if err != nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
			continue
		}
		out = append(out, ip.String())
		if len(out) == 16 {
			break
		}
	}
	return out
}
func text(s string, n int) bool { return len(s) <= n && strings.IndexFunc(s, unicode.IsControl) < 0 }
func (r Report) Validate(now time.Time) error {
	bad := func() error { return errors.New("invalid device telemetry") }
	if !text(r.AgentVersion, 64) || r.AgentVersion == "" || r.CollectedAt.IsZero() || r.CollectedAt.Before(now.Add(-2*time.Minute)) || r.CollectedAt.After(now.Add(2*time.Minute)) || r.UptimeSeconds < 0 || r.UptimeSeconds > 20*365*86400 {
		return bad()
	}
	s := r.System
	if !text(s.Hostname, 256) || !text(s.OS, 32) || !text(s.OSVersion, 256) || !text(s.Arch, 32) || !text(s.CPUModel, 256) || s.CPUCores < 1 || s.CPUCores > 4096 || len(s.GPUs) > 8 || len(s.Displays) > 16 || len(r.LocalIPs) > 16 {
		return bad()
	}
	if s.HardwareProbe != "ok" && s.HardwareProbe != "partial" && s.HardwareProbe != "unavailable" {
		return bad()
	}
	for _, g := range s.GPUs {
		if !text(g.Name, 256) || !text(g.Driver, 128) {
			return bad()
		}
	}
	for _, d := range s.Displays {
		if !text(d.Name, 256) || d.Width > 32768 || d.Height > 32768 || d.Refresh > 2000 {
			return bad()
		}
	}
	for _, ip := range r.LocalIPs {
		if net.ParseIP(ip) == nil {
			return bad()
		}
	}
	if r.Hosting.Mode != "interactive" && r.Hosting.Mode != "service" && r.Hosting.Mode != "diagnostic" {
		return bad()
	}
	m := r.Sample
	if m.At.IsZero() || m.At.Before(now.Add(-2*time.Minute)) || m.At.After(now.Add(2*time.Minute)) {
		return bad()
	}
	for _, v := range []*float64{m.CPUPercent, m.RXPerSecond, m.TXPerSecond} {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0) {
			return bad()
		}
	}
	if m.CPUPercent != nil && *m.CPUPercent > 100 {
		return bad()
	}
	for _, pair := range [][2]*uint64{{m.MemoryUsed, m.MemoryTotal}, {m.DiskUsed, m.DiskTotal}} {
		if pair[0] != nil && pair[1] != nil && *pair[0] > *pair[1] {
			return bad()
		}
	}
	return nil
}
