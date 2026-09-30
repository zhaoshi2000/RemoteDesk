package agent

import (
	"context"
	"log/slog"
	"net"
	"os"
	"remotedesk.local/remotedesk/internal/monitor"
	"remotedesk.local/remotedesk/internal/telemetry"
	"strconv"
	"time"
)

func (r *runtime) reportTelemetry(service bool) {
	if r.cfg.DisableTelemetry {
		return
	}
	started := time.Now()
	system := telemetry.Inspect(r.ctx)
	collector := monitor.New(r.dir)
	timer := time.NewTicker(15 * time.Second)
	defer timer.Stop()
	for {
		report := telemetry.Report{AgentVersion: telemetry.AgentVersion, CollectedAt: time.Now().UTC(), UptimeSeconds: int64(time.Since(started).Seconds()), System: system, LocalIPs: telemetry.LocalIPs(), Sample: collector.Collect()}
		mode := "interactive"
		if service {
			mode = "service"
		}
		report.Hosting = telemetry.Hosting{Mode: mode, DesktopEnabled: r.cfg.DesktopEnabled, FileSharingEnabled: r.cfg.ReceiveDirectory != ""}
		if st, e := os.Stat(r.cfg.MediaExecutable); e == nil && st.Mode().IsRegular() {
			report.Hosting.MediaWorkerPresent = true
		}
		ctx, cancel := context.WithTimeout(r.ctx, 700*time.Millisecond)
		conn, e := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(r.cfg.SSHPort)))
		cancel()
		if e == nil {
			report.Hosting.SSHListening = true
			conn.Close()
		}
		ctx, cancel = context.WithTimeout(r.ctx, 4*time.Second)
		e = r.api.Do(ctx, "POST", "/v1/telemetry", report, nil)
		cancel()
		if e != nil && r.ctx.Err() == nil {
			slog.Debug("device telemetry unavailable", "error", e)
		}
		select {
		case <-r.ctx.Done():
			return
		case <-timer.C:
		}
	}
}
