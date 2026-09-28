package media

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"remotedesk.local/remotedesk/internal/identity"
	"remotedesk.local/remotedesk/internal/tunnel"
)

type NativeOptions struct {
	Executable                                       string
	Host, Control, Clipboard, Audio, RequireHardware bool
	Adapter, Monitor, Width, Height, FPS             int
	Bitrate                                          int
	ParentWindow                                     uint64
}

// Configuration is passed over an inherited anonymous pipe. No private keys on command lines,
// network listeners, temporary files or log messages.
func NativeConfig(id *identity.Identity, peer []byte, port int, o NativeOptions) ([]byte, error) {
	if len(peer) != 32 || port < 1 || port > 65535 {
		return nil, errors.New("bad native peer configuration")
	}
	if o.Width == 0 {
		o.Width = 1920
	}
	if o.Height == 0 {
		o.Height = 1080
	}
	if o.FPS == 0 {
		o.FPS = 60
	}
	if o.Bitrate == 0 {
		o.Bitrate = 12_000_000
	}
	if o.Adapter < 0 || o.Adapter > 31 || o.Monitor < 0 || o.Monitor > 31 || o.Width < 16 || o.Width > 8192 || o.Height < 16 || o.Height > 8192 || o.Width%2 != 0 || o.Height%2 != 0 || o.FPS < 1 || o.FPS > 120 || o.Bitrate < 250000 || o.Bitrate > 100000000 {
		return nil, errors.New("video settings out of range")
	}
	cert, e := tunnel.Certificate(id)
	if e != nil {
		return nil, e
	}
	key, e := x509.MarshalPKCS8PrivateKey(id.Private)
	if e != nil {
		return nil, e
	}
	cp := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	kp := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
	h := make([]byte, 72)
	copy(h, "RDC2")
	var flags uint32
	if o.Host {
		flags |= 1
	}
	if o.Control {
		flags |= 2
	}
	if o.Clipboard {
		flags |= 4
	}
	if o.Audio {
		flags |= 8
	}
	if o.RequireHardware {
		flags |= 16
	}
	binary.BigEndian.PutUint32(h[4:], flags)
	for i, n := range []int{port, o.Adapter, o.Monitor, o.Width, o.Height, o.FPS} {
		binary.BigEndian.PutUint16(h[8+2*i:], uint16(n))
	}
	binary.BigEndian.PutUint32(h[20:], uint32(o.Bitrate))
	binary.BigEndian.PutUint64(h[24:], o.ParentWindow)
	binary.BigEndian.PutUint32(h[32:], uint32(len(cp)))
	binary.BigEndian.PutUint32(h[36:], uint32(len(kp)))
	copy(h[40:], peer)
	h = append(h, cp...)
	h = append(h, kp...)
	for i := range key {
		key[i] = 0
	}
	for i := range kp {
		kp[i] = 0
	}
	return h, nil
}
func RunNative(ctx context.Context, id *identity.Identity, peer []byte, b *Broker, o NativeOptions) error {
	if o.Executable == "" {
		return errors.New("media_executable is not configured; build and install remote-media first")
	}
	exe, e := filepath.Abs(o.Executable)
	if e != nil {
		return e
	}
	fi, e := os.Stat(exe)
	if e != nil {
		return fmt.Errorf("native media executable: %w", e)
	}
	if !fi.Mode().IsRegular() {
		return errors.New("media executable is not regular")
	}
	config, e := NativeConfig(id, peer, b.Port(), o)
	if e != nil {
		return e
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(childCtx, exe, "--ipc")
	gracefulNative(cmd)
	cmd.Stderr = os.Stderr
	stdin, e := cmd.StdinPipe()
	if e != nil {
		return e
	}
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		return e
	}
	if e = cmd.Start(); e != nil {
		return e
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	if e = writeAll(stdin, config); e != nil {
		cancel()
		<-wait
		return e
	}
	for i := range config {
		config[i] = 0
	}
	stdin.Close()
	ready := make(chan error, 1)
	go func() {
		r := bufio.NewReaderSize(stdout, 4096)
		line, e := r.ReadString('\n')
		if e == nil {
			parts := strings.Fields(line)
			if len(parts) != 2 || parts[0] != "READY" {
				e = errors.New("native media did not emit readiness")
			} else {
				var port int
				port, e = strconv.Atoi(parts[1])
				if e == nil {
					e = b.SetNativePort(port)
				}
			}
		}
		ready <- e
		if e == nil {
			_, _ = io.Copy(os.Stdout, r)
		}
	}()
	select {
	case e = <-ready:
		if e != nil {
			cancel()
			<-wait
			return e
		}
	case <-time.After(20 * time.Second):
		cancel()
		<-wait
		return errors.New("native media startup timeout")
	case <-ctx.Done():
		cancel()
		<-wait
		return ctx.Err()
	}
	return <-wait
}
