package telemetry

import (
	"context"
	"math"
	"remotedesk.local/remotedesk/internal/monitor"
	"testing"
	"time"
)

func validReport() Report {
	now := time.Now().UTC()
	return Report{AgentVersion: AgentVersion, CollectedAt: now, System: System{OS: "test", Arch: "test", CPUCores: 1, HardwareProbe: "partial"}, LocalIPs: []string{"127.0.0.1"}, Hosting: Hosting{Mode: "interactive"}, Sample: monitor.Sample{At: now}}
}
func TestReportBounds(t *testing.T) {
	if e := validReport().Validate(time.Now()); e != nil {
		t.Fatal(e)
	}
	cases := map[string]func(*Report){"old sample": func(r *Report) { r.Sample.At = time.Now().Add(-time.Hour) }, "future report": func(r *Report) { r.CollectedAt = time.Now().Add(time.Hour) }, "oversize name": func(r *Report) { r.System.Hostname = string(make([]byte, 500)) }, "too many cores": func(r *Report) { r.System.CPUCores = 99999 }, "invalid IP": func(r *Report) { r.LocalIPs = []string{"not an IP"} }, "nan CPU": func(r *Report) { v := math.NaN(); r.Sample.CPUPercent = &v }, "cpu out of range": func(r *Report) { v := 101.0; r.Sample.CPUPercent = &v }, "negative uptime": func(r *Report) { r.UptimeSeconds = -1 }, "memory overflow": func(r *Report) { a, b := uint64(11), uint64(10); r.Sample.MemoryUsed = &a; r.Sample.MemoryTotal = &b }}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := validReport()
			change(&r)
			if r.Validate(time.Now()) == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
}
func TestActualLocalInspectionHasNoSecrets(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
	defer cancel()
	s := Inspect(ctx)
	if s.OS == "" || s.Arch == "" || s.CPUCores < 1 {
		t.Fatal("missing actual platform fields")
	}
	r := validReport()
	r.System = s
	r.LocalIPs = LocalIPs()
	if e := r.Validate(time.Now()); e != nil {
		t.Fatal(e)
	}
}
