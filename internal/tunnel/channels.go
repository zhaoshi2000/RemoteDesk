package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"remotedesk.local/remotedesk/internal/identity"
	"time"
)

const FileALPN = "remotedesk-file/2"

func verifyChannel(cs tls.ConnectionState, allow func(string) bool, alpn string) error {
	if cs.NegotiatedProtocol != alpn {
		return errors.New("wrong channel protocol")
	}
	copy := cs
	copy.NegotiatedProtocol = ALPN
	return VerifyPeer(copy, allow)
}
func FileClient(ctx context.Context, raw net.Conn, id *identity.Identity, key string) (net.Conn, error) {
	cert, e := Certificate(id)
	if e != nil {
		raw.Close()
		return nil, e
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, NextProtos: []string{FileALPN}, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		return verifyChannel(cs, func(k string) bool { return k == key }, FileALPN)
	}}
	c := tls.Client(raw, cfg)
	_ = c.SetDeadline(time.Now().Add(8 * time.Second))
	if e = c.HandshakeContext(ctx); e != nil {
		c.Close()
		return nil, e
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

// ServeMultiplex dispatches after authenticated ALPN negotiation. SSH bytes can
// never be reinterpreted as file commands; permissions are checked per channel.
func ServeMultiplex(ctx context.Context, raw net.Conn, id *identity.Identity, allow func(string, string) bool, sshPort int, files func(net.Conn, string) error) error {
	defer raw.Close()
	cert, e := Certificate(id)
	if e != nil {
		return e
	}
	var peer string
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, NextProtos: []string{ALPN, FileALPN}, ClientAuth: tls.RequireAnyClientCert, VerifyConnection: func(cs tls.ConnectionState) error {
		kind := "ssh"
		if cs.NegotiatedProtocol == FileALPN {
			kind = "file"
		}
		return verifyChannel(cs, func(k string) bool { peer = k; return allow(k, kind) }, map[string]string{"ssh": ALPN, "file": FileALPN}[kind])
	}}
	c := tls.Server(raw, cfg)
	_ = c.SetDeadline(time.Now().Add(8 * time.Second))
	if e = c.HandshakeContext(ctx); e != nil {
		return e
	}
	if c.ConnectionState().NegotiatedProtocol == FileALPN {
		_ = c.SetDeadline(time.Time{})
		return files(c, peer)
	}
	return serveSSH(ctx, c, sshPort)
}
