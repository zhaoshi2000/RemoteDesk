package main

import (
	"context"
	"flag"
	"net"
	"os"
	"path/filepath"
	"remotedesk.local/remotedesk/internal/apiclient"
	"remotedesk.local/remotedesk/internal/identity"
	"remotedesk.local/remotedesk/internal/monitor"
	"remotedesk.local/remotedesk/internal/telemetry"
	"strconv"
	"time"
)

func diagnostics(args []string) error {
	f := flag.NewFlagSet("diagnostics", flag.ContinueOnError)
	state := f.String("state", "state/agent", "private state")
	hardware := f.Bool("hardware", false, "include fixed local hardware inspection")
	if e := f.Parse(args); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 16*time.Second)
	defer cancel()
	c := monitor.New(*state)
	c.Collect()
	time.Sleep(120 * time.Millisecond)
	out := map[string]any{"agent_version": telemetry.AgentVersion, "sample": c.Collect(), "initialized": false, "server_reachable": false, "reported_scope": "hardware/resource metadata only"}
	if *hardware {
		out["system"] = telemetry.Inspect(ctx)
	}
	id, cfg, e := identity.Load(*state)
	if e != nil {
		return printJSON(out)
	}
	out["initialized"] = true
	out["device_id"] = id.ID
	out["name"] = cfg.Name
	out["server"] = cfg.Server
	out["telemetry_enabled"] = !cfg.DisableTelemetry
	out["desktop_enabled"] = cfg.DesktopEnabled
	out["file_sharing_enabled"] = cfg.ReceiveDirectory != ""
	if st, e := os.Stat(filepath.Join(filepath.Dir(os.Args[0]), "remote-media.exe")); e == nil {
		out["media_worker_present"] = st.Mode().IsRegular()
	}
	conn, e := (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.SSHPort)))
	out["ssh_listening"] = e == nil
	if conn != nil {
		conn.Close()
	}
	api, e := apiclient.New(id, cfg)
	if e == nil {
		defer api.Close()
		cctx, stop := context.WithTimeout(ctx, 4*time.Second)
		defer stop()
		start := time.Now()
		peer, err := api.Peer(cctx, id.ID)
		if err == nil {
			out["server_reachable"] = true
			out["server_request_ms"] = time.Since(start).Milliseconds()
			out["online"] = peer.Online
			out["last_seen"] = peer.LastSeen
		} else {
			out["coordinator_error"] = err.Error()
		}
	}
	return printJSON(out)
}
