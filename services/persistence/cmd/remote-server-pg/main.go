package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"remotedesk.local/remotedesk/internal/identity"
	"remotedesk.local/remotedesk/internal/server"
	"remotedesk.local/remotedesk/services/persistence/backend"
)

func main() {
	if e := execute(); e != nil {
		fmt.Fprintln(os.Stderr, "error:", e)
		os.Exit(1)
	}
}
func execute() error {
	if len(os.Args) < 2 {
		return errors.New("usage: remote-server init|run|version")
	}
	switch os.Args[1] {
	case "version":
		fmt.Println("RemoteDesk coordinator 0.2.0 PostgreSQL + Redis (single signaling process)")
		return nil
	case "init":
		f := flag.NewFlagSet("init", flag.ContinueOnError)
		dir := f.String("dir", "state/server", "private server state directory")
		listen := f.String("listen", "127.0.0.1:8443", "HTTPS listen address")
		udp := f.String("stun", "127.0.0.1:3478", "IPv4 STUN listen address")
		hosts := f.String("hosts", "localhost,127.0.0.1", "comma-separated TLS certificate DNS names/IPs")
		if e := f.Parse(os.Args[2:]); e != nil {
			return e
		}
		c, e := server.InitConfig(*dir, *listen, *udp, *hosts)
		if e != nil {
			return e
		}
		fmt.Println("Created private configuration:", filepath.Join(filepath.Dir(c.TLSCert), "server.json"))
		fmt.Println("Distribute ONLY this public certificate to agents:", c.TLSCert)
		fmt.Println("Enrollment/admin tokens are in private .token files; do not publish the state directory.")
		return nil
	case "run":
		f := flag.NewFlagSet("run", flag.ContinueOnError)
		path := f.String("config", "state/server/server.json", "server config path")
		if e := f.Parse(os.Args[2:]); e != nil {
			return e
		}
		var cfg server.Config
		if e := identity.ReadJSON(*path, &cfg); e != nil {
			return e
		}
		initCtx, initCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer initCancel()
		st, e := backend.Open(initCtx, os.Getenv("RD_POSTGRES_URL"))
		if e != nil {
			return e
		}
		defer st.Pool.Close()
		presence, e := backend.OpenRedis(initCtx, os.Getenv("RD_REDIS_URL"))
		if e != nil {
			return e
		}
		defer presence.Client.Close()
		app, e := server.NewWithBackends(cfg, st, presence, st)
		if e != nil {
			return e
		}
		defer app.Close()
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		udpAddr, e := net.ResolveUDPAddr("udp4", cfg.STUN)
		if e != nil {
			return e
		}
		udp, e := net.ListenUDP("udp4", udpAddr)
		if e != nil {
			return e
		}
		defer udp.Close()
		errCh := make(chan error, 2)
		go func() { errCh <- app.ServeMediaUDP(ctx, udp) }()
		srv := &http.Server{Addr: cfg.Listen, Handler: app, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}, TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){}, ReadHeaderTimeout: 8 * time.Second, ReadTimeout: 25 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
		go func() { errCh <- srv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey) }()
		app.LogReady()
		select {
		case <-ctx.Done():
		case e = <-errCh:
			if e != nil && !errors.Is(e, http.ErrServerClosed) {
				cancel()
				_ = srv.Close()
				return e
			}
		}
		cancel()
		app.Close()
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		slog.Info("coordinator stopping")
		return srv.Shutdown(shutdownCtx)
	default:
		return errors.New("unknown command; use init, run or version")
	}
}
