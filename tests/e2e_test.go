package tests

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"remotedesk.local/remotedesk/internal/agent"
	"remotedesk.local/remotedesk/internal/apiclient"
	"remotedesk.local/remotedesk/internal/identity"
	"remotedesk.local/remotedesk/internal/protocol"
	"remotedesk.local/remotedesk/internal/server"
	"remotedesk.local/remotedesk/internal/store"
	"remotedesk.local/remotedesk/internal/stun"
	"remotedesk.local/remotedesk/internal/tunnel"
)

type world struct {
	a, b   string
	ia, ib *identity.Identity
	ca, cb *apiclient.Client
	app    *server.Server
	cfg    server.Config
	ctx    context.Context
	cancel context.CancelFunc
	url    string
}

func setup(t *testing.T, running bool) *world {
	t.Helper()
	root := t.TempDir()
	cfg, e := server.InitConfig(filepath.Join(root, "server"), "127.0.0.1:0", "127.0.0.1:0", "localhost,127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	st, e := store.Open(cfg.Store)
	if e != nil {
		t.Fatal(e)
	}
	app, e := server.New(cfg, st)
	if e != nil {
		t.Fatal(e)
	}
	cert, e := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
	if e != nil {
		t.Fatal(e)
	}
	ts := httptest.NewUnstartedServer(app)
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}
	ts.StartTLS()
	ctx, cancel := context.WithCancel(context.Background())
	u, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	go func() { _ = stun.Serve(ctx, u) }()
	echo, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	go func() {
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				stop := context.AfterFunc(ctx, func() { c.Close() })
				defer stop()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	sshPort := echo.Addr().(*net.TCPAddr).Port
	w := &world{a: filepath.Join(root, "a"), b: filepath.Join(root, "b"), app: app, cfg: cfg, ctx: ctx, cancel: cancel, url: ts.URL}
	ac := identity.Config{Server: ts.URL, CAFile: cfg.TLSCert, Name: "A", STUN: u.LocalAddr().String(), ListenTCP: "127.0.0.1:0", ListenUDP: "127.0.0.1:0", SSHPort: sshPort}
	w.ia, e = identity.Init(w.a, ac)
	if e != nil {
		t.Fatal(e)
	}
	bc := ac
	bc.Name = "B"
	w.ib, e = identity.Init(w.b, bc)
	if e != nil {
		t.Fatal(e)
	}
	w.ca, e = apiclient.New(w.ia, ac)
	if e != nil {
		t.Fatal(e)
	}
	w.cb, e = apiclient.New(w.ib, bc)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.ca.Register(ctx, cfg.EnrollmentToken); e != nil {
		t.Fatal(e)
	}
	if _, e = w.cb.Register(ctx, cfg.EnrollmentToken); e != nil {
		t.Fatal(e)
	}
	if e = identity.Trust(w.a, filepath.Join(w.b, "public-identity.json"), true, true); e != nil {
		t.Fatal(e)
	}
	if e = identity.Trust(w.b, filepath.Join(w.a, "public-identity.json"), true, true); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		u.Close()
		echo.Close()
		app.Close()
		ts.Close()
		w.ca.Close()
		w.cb.Close()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("agent goroutines did not shut down")
		}
	})
	if running {
		for _, dir := range []string{w.a, w.b} {
			wg.Add(1)
			go func(path string) {
				defer wg.Done()
				if e := agent.Run(ctx, path); e != nil && ctx.Err() == nil {
					t.Errorf("agent failed: %v", e)
				}
			}(dir)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			a, ea := w.ca.Peer(ctx, w.ia.ID)
			b, eb := w.ca.Peer(ctx, w.ib.ID)
			if ea == nil && eb == nil && a.Online && b.Online {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("agents failed to become online")
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	return w
}
func transfer(t *testing.T, c net.Conn) {
	t.Helper()
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	payload := make([]byte, 128<<10)
	_, _ = rand.Read(payload)
	sent := make(chan error, 1)
	go func() { _, e := c.Write(payload); sent <- e }()
	got := make([]byte, len(payload))
	if _, e := io.ReadFull(c, got); e != nil {
		t.Fatal(e)
	}
	if e := <-sent; e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(payload, got) {
		t.Fatal("tunnel payload mismatch")
	}
}
func TestEndToEndDirectTLS(t *testing.T) {
	w := setup(t, true)
	ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
	defer cancel()
	c, mode, e := agent.DialPeer(ctx, w.a, w.ib.ID, false)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(mode, "Direct") {
		t.Fatalf("expected direct, got %s", mode)
	}
	transfer(t, c)
}
func TestEndToEndEncryptedRelay(t *testing.T) {
	w := setup(t, true)
	ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
	defer cancel()
	c, mode, e := agent.DialPeer(ctx, w.a, w.ib.ID, true)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(mode, "Relay") {
		t.Fatalf("expected relay, got %s", mode)
	}
	transfer(t, c)
}
func TestSignedUDPReachabilityBothDirections(t *testing.T) {
	w := setup(t, true)
	ctx, cancel := context.WithTimeout(w.ctx, 6*time.Second)
	defer cancel()
	status, e := agent.Probe(ctx, w.a, w.ib.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !status.AReachable || !status.BReachable {
		t.Fatal("not both directions reachable")
	}
}
func TestTargetLocalACLPreventsUnauthorizedTLS(t *testing.T) {
	w := setup(t, true)
	if e := identity.Untrust(w.b, w.ia.ID); e != nil {
		t.Fatal(e)
	}
	p, e := w.ca.Peer(w.ctx, w.ib.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := net.DialTimeout("tcp", p.Candidates.TCP[0], time.Second)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(w.ctx, 2*time.Second)
	defer cancel()
	c, e := tunnel.Client(ctx, raw, w.ia, protocol.PublicString(w.ib.Public))
	if e == nil {
		c.Close()
		t.Fatal("target accepted a revoked/unauthorized controller")
	}
}
func TestTLSRejectsWrongPinnedTarget(t *testing.T) {
	w := setup(t, true)
	p, e := w.ca.Peer(w.ctx, w.ib.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := net.DialTimeout("tcp", p.Candidates.TCP[0], time.Second)
	if e != nil {
		t.Fatal(e)
	}
	wrong, _, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(w.ctx, 2*time.Second)
	defer cancel()
	c, e := tunnel.Client(ctx, raw, w.ia, protocol.PublicString(wrong))
	if e == nil {
		c.Close()
		t.Fatal("wrong target key accepted")
	}
}
func TestReplayRejectedAndAdminProtected(t *testing.T) {
	w := setup(t, false)
	r, e := w.ca.Request(w.ctx, "GET", "/v1/peer?id="+w.ib.ID, nil, "")
	if e != nil {
		t.Fatal(e)
	}
	r2 := r.Clone(w.ctx)
	resp, e := w.ca.HTTP.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	resp, e = w.ca.HTTP.Do(r2)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("replay status %d", resp.StatusCode)
	}
	e = w.ca.Do(w.ctx, "GET", "/v1/admin/overview", nil, &map[string]any{})
	var he *apiclient.HTTPError
	if !errors.As(e, &he) || he.Status != 401 {
		t.Fatalf("admin endpoint leaked to device: %v", e)
	}
}
func TestTamperedBodyRejected(t *testing.T) {
	w := setup(t, false)
	r, e := w.ca.Request(w.ctx, "POST", "/v1/heartbeat", map[string]string{"device_id": w.ia.ID}, "")
	if e != nil {
		t.Fatal(e)
	}
	payload := []byte(`{"device_id":"attacker"}`)
	r.Body = io.NopCloser(bytes.NewReader(payload))
	r.ContentLength = int64(len(payload))
	resp, e := w.ca.HTTP.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("tampered body status %d", resp.StatusCode)
	}
}
func TestRelayTicketIsSingleUse(t *testing.T) {
	w := setup(t, true)
	session, e := w.ca.CreateSession(w.ctx, w.ib.ID, "ssh")
	if e != nil {
		t.Fatal(e)
	}
	first, e := w.ca.Relay(w.ctx, session.ID, "a", session.Ticket)
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	second, e := w.ca.Relay(w.ctx, session.ID, "a", session.Ticket)
	if e == nil {
		second.Close()
		t.Fatal("relay ticket reused")
	}
}
func TestRegistrationNeedsEnrollmentSecret(t *testing.T) {
	w := setup(t, false)
	_, e := w.ca.Register(w.ctx, "wrong-token")
	var he *apiclient.HTTPError
	if !errors.As(e, &he) || he.Status != 401 {
		t.Fatalf("missing enrollment rejected incorrectly: %v", e)
	}
}
func TestListenerNeverExposesSSHForwarder(t *testing.T) {
	for _, address := range []string{"0.0.0.0:0", "[::]:0", "192.168.1.2:22", "localhost:0"} {
		ln, e := agent.ListenLoopback(address)
		if e == nil {
			ln.Close()
			t.Errorf("unsafe forwarding address accepted: %s", address)
		}
	}
	ln, e := agent.ListenLoopback("127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ln.Close()
}
func TestCoordinatorDoesNotHoldDevicePrivateKeys(t *testing.T) {
	w := setup(t, false)
	data, e := os.ReadFile(w.cfg.Store)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(data, w.ia.Private) || bytes.Contains(data, w.ib.Private) || bytes.Contains(data, []byte("PRIVATE KEY")) {
		t.Fatal("device private key persisted on coordinator")
	}
}

func TestAutomaticRelayFallbackAfterDirectFailure(t *testing.T) {
	w := setup(t, true)
	peer, e := w.cb.Peer(w.ctx, w.ib.ID)
	if e != nil {
		t.Fatal(e)
	}
	unused, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := unused.Addr().String()
	unused.Close()
	candidates := peer.Candidates
	candidates.TCP = []string{address}
	candidates.IssuedAt = time.Now().Unix()
	candidates.Sign(w.ib.Private)
	if e = w.cb.Do(w.ctx, "POST", "/v1/heartbeat", candidates, nil); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
	defer cancel()
	conn, mode, e := agent.DialPeer(ctx, w.a, w.ib.ID, false)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(mode, "Relay") {
		conn.Close()
		t.Fatalf("expected automatic fallback, got %s", mode)
	}
	transfer(t, conn)
}
