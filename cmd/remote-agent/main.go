package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"remotedesk.local/remotedesk/internal/agent"
	"remotedesk.local/remotedesk/internal/apiclient"
	"remotedesk.local/remotedesk/internal/identity"
	"remotedesk.local/remotedesk/internal/media"
	"remotedesk.local/remotedesk/internal/winservice"
)

func main() {
	if e := execute(); e != nil {
		fmt.Fprintln(os.Stderr, "error:", e)
		os.Exit(1)
	}
}
func printJSON(v any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
func execute() error {
	if len(os.Args) < 2 {
		return errors.New("usage: remote-agent init|register|identity|trust|grant|untrust|configure-host|peers|run|desktop|file|probe|tunnel|ssh|service|version")
	}
	command := os.Args[1]
	if command == "diagnostics" {
		return diagnostics(os.Args[2:])
	}
	if command == "configure-host" {
		return configureHost(os.Args[2:])
	}
	if command == "file" || command == "peers" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if command == "file" {
			return fileCommand(ctx, os.Args[2:])
		}
		return listPeers(ctx, os.Args[2:])
	}

	if command == "version" {
		fmt.Println("RemoteDesk agent 0.4.0-preview; authenticated media broker + SSH + scoped telemetry")
		return nil
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	state := f.String("state", "state/agent", "private device state directory")
	var serverURL, ca, name, stunAddr, listenTCP, listenUDP, tokenFile, peerFile, peer, listen, user *string
	var sshPort *int
	var grantSSH, grantProbe, relayOnly *bool
	var desktop, control, clipboard, audio, files *bool
	var engine *string
	var width, height, fps, bitrate, adapter, monitor *int
	var parentWindow *uint64
	switch command {
	case "init":
		hostname, _ := os.Hostname()
		serverURL = f.String("server", "", "HTTPS coordinator origin")
		ca = f.String("ca", "", "trusted coordinator certificate/CA file (copied into state)")
		name = f.String("name", hostname, "device name")
		stunAddr = f.String("stun", "", "STUN server host:port")
		listenTCP = f.String("listen-tcp", "0.0.0.0:0", "authenticated peer TCP listener")
		listenUDP = f.String("listen-udp", "0.0.0.0:0", "IPv4 probe listener")
		sshPort = f.Int("ssh-port", 22, "local loopback OpenSSH port")
	case "register":
		tokenFile = f.String("token-file", "", "enrollment token file (or RD_ENROLL_TOKEN environment)")
	case "trust":
		peerFile = f.String("peer-file", "", "peer public-identity.json obtained through a trusted channel")
		grantSSH = f.Bool("ssh", true, "grant SSH device authorization")
		grantProbe = f.Bool("probe", true, "grant UDP probing authorization")
	case "grant":
		peer = f.String("peer", "", "verified peer device ID")
		desktop = f.Bool("desktop", false, "grant desktop viewing")
		control = f.Bool("control", false, "grant keyboard/mouse")
		clipboard = f.Bool("clipboard", false, "grant text clipboard")
		audio = f.Bool("audio", false, "grant system audio")
		files = f.Bool("files", false, "grant file transfer")
	case "desktop":
		peer = f.String("peer", "", "target device ID")
		relayOnly = f.Bool("relay-only", false, "force relay")
		engine = f.String("engine", "", "native media executable override")
		width = f.Int("width", 1920, "video width")
		height = f.Int("height", 1080, "video height")
		fps = f.Int("fps", 60, "frames per second")
		bitrate = f.Int("bitrate", 12000000, "bitrate bits/s")
		adapter = f.Int("adapter", 0, "GPU adapter")
		monitor = f.Int("monitor", 0, "display index")
		parentWindow = f.Uint64("parent-window", 0, "Qt viewer canvas HWND")
		control = f.Bool("control", true, "enable input when peer permits")
		clipboard = f.Bool("clipboard", false, "enable text clipboard")
		audio = f.Bool("audio", false, "enable system audio")
	case "untrust", "probe":
		peer = f.String("peer", "", "target device ID")
	case "tunnel", "ssh":
		peer = f.String("peer", "", "target device ID")
		listen = f.String("listen", "127.0.0.1:0", "loopback forwarding address")
		relayOnly = f.Bool("relay-only", false, "skip TCP LAN direct attempts; force encrypted relay")
		if command == "ssh" {
			user = f.String("user", "", "Windows/OpenSSH username")
		}
	case "run", "identity", "service":
	default:
		return errors.New("unknown command")
	}
	if e := f.Parse(os.Args[2:]); e != nil {
		return e
	}
	dir, e := filepath.Abs(*state)
	if e != nil {
		return e
	}
	if command == "init" {
		var certificate []byte
		caPath := ""
		if *ca != "" {
			certificate, e = os.ReadFile(*ca)
			if e != nil {
				return e
			}
			caPath = "coordinator.crt"
		}
		cfg := identity.Config{Server: *serverURL, CAFile: caPath, Name: *name, STUN: *stunAddr, ListenTCP: *listenTCP, ListenUDP: *listenUDP, SSHPort: *sshPort}
		id, e := identity.Init(dir, cfg)
		if e != nil {
			return e
		}
		if len(certificate) > 0 {
			if e = identity.WriteAtomic(filepath.Join(dir, caPath), certificate, 0600); e != nil {
				return e
			}
		}
		fmt.Println("Device ID:", id.ID)
		fmt.Println("Public identity export:", filepath.Join(dir, "public-identity.json"))
		fmt.Println("Next: register, exchange/verify public identities, trust, then run.")
		return nil
	}
	if command == "service" {
		return winservice.Run("RemoteDeskAgent", func(ctx context.Context) error { return agent.RunService(ctx, dir) })
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch command {
	case "grant":
		return identity.GrantCapabilities(dir, *peer, *desktop, *control, *clipboard, *audio, *files)
	case "desktop":
		return agent.Desktop(ctx, dir, *peer, *relayOnly, media.NativeOptions{Executable: *engine, Width: *width, Height: *height, FPS: *fps, Bitrate: *bitrate, Adapter: *adapter, Monitor: *monitor, ParentWindow: *parentWindow, Control: *control, Clipboard: *clipboard, Audio: *audio, RequireHardware: true})
	case "identity":
		var p identity.PublicIdentity
		if e = identity.ReadJSON(filepath.Join(dir, "public-identity.json"), &p); e != nil {
			return e
		}
		return printJSON(p)
	case "trust":
		if *peerFile == "" {
			return errors.New("--peer-file required; verify the fingerprint through a trusted channel before importing")
		}
		if e = identity.Trust(dir, *peerFile, *grantSSH, *grantProbe); e != nil {
			return e
		}
		fmt.Println("Peer key explicitly authorized. Windows/OpenSSH user authentication is still required.")
		return nil
	case "untrust":
		if e = identity.Untrust(dir, *peer); e != nil {
			return e
		}
		fmt.Println("Authorization removed for new connections. Restart the agent to close existing sessions.")
		return nil
	case "register":
		token := strings.TrimSpace(os.Getenv("RD_ENROLL_TOKEN"))
		if *tokenFile != "" {
			b, e := os.ReadFile(*tokenFile)
			if e != nil {
				return e
			}
			token = strings.TrimSpace(string(b))
		}
		if token == "" {
			return errors.New("provide --token-file or RD_ENROLL_TOKEN")
		}
		id, cfg, e := identity.Load(dir)
		if e != nil {
			return e
		}
		client, e := apiclient.New(id, cfg)
		if e != nil {
			return e
		}
		defer client.Close()
		d, e := client.Register(ctx, token)
		if e != nil {
			return e
		}
		return printJSON(d)
	case "run":
		return agent.Run(ctx, dir)
	case "probe":
		pctx, stop := context.WithTimeout(ctx, 16*time.Second)
		defer stop()
		status, e := agent.Probe(pctx, dir, *peer)
		_ = printJSON(status)
		if e != nil {
			return e
		}
		fmt.Println("Both signed UDP directions are reachable. This is NOT a QUIC desktop session.")
		return nil
	case "tunnel", "ssh":
		if *peer == "" {
			return errors.New("--peer required")
		}
		ln, e := agent.ListenLoopback(*listen)
		if e != nil {
			return e
		}
		defer ln.Close()
		fmt.Println("Local SSH tunnel:", ln.Addr().String())
		if command == "tunnel" {
			fmt.Println("Keep this process running. Normal OpenSSH host-key and user authentication still apply.")
			return agent.Forward(ctx, ln, dir, *peer, *relayOnly)
		}
		if *user == "" {
			return errors.New("--user required")
		}
		ssh, e := exec.LookPath("ssh")
		if e != nil {
			return errors.New("OpenSSH client not found in PATH")
		}
		childCtx, stop := context.WithCancel(ctx)
		defer stop()
		done := make(chan error, 1)
		go func() { done <- agent.Forward(childCtx, ln, dir, *peer, *relayOnly) }()
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		args := []string{"-p", port, "-l", *user, "-o", "HostKeyAlias=remotedesk-" + *peer, "-o", "CheckHostIP=no", "-o", "StrictHostKeyChecking=ask", "-o", "UserKnownHostsFile=" + filepath.Join(dir, "known_hosts"), "-o", "ServerAliveInterval=30", "127.0.0.1"}
		cmd := exec.CommandContext(childCtx, ssh, args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		e = cmd.Run()
		stop()
		<-done
		return e
	}
	return errors.New("unhandled command")
}
