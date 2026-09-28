// Package tunnel supplies a dedicated TLS 1.3 SSH byte tunnel, NOT a video transport.
package tunnel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"time"

	"remotedesk.local/remotedesk/internal/bridge"
	"remotedesk.local/remotedesk/internal/identity"
	"remotedesk.local/remotedesk/internal/protocol"
)

const ALPN = "remotedesk-ssh/1"

var preface = []byte{'R', 'D', 'S', 'K', 1, 6, 0, 0}

func Certificate(id *identity.Identity) (tls.Certificate, error) {
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return tls.Certificate{}, e
	}
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: id.ID}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, tpl, tpl, id.Public, id.Private)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: id.Private}, e
}
func VerifyPeer(cs tls.ConnectionState, allow func(string) bool) error {
	if len(cs.PeerCertificates) != 1 {
		return errors.New("exactly one device certificate is required")
	}
	c := cs.PeerCertificates[0]
	pub, ok := c.PublicKey.(ed25519.PublicKey)
	if !ok || !allow(protocol.PublicString(pub)) {
		return errors.New("device public key is not locally authorized")
	}
	now := time.Now()
	if now.Before(c.NotBefore) || now.After(c.NotAfter) {
		return errors.New("expired peer certificate")
	}
	if e := c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature); e != nil {
		return errors.New("invalid device certificate self-signature")
	}
	if cs.NegotiatedProtocol != ALPN {
		return errors.New("wrong tunnel protocol")
	}
	return nil
}
func Client(ctx context.Context, raw net.Conn, id *identity.Identity, expectedKey string) (net.Conn, error) {
	cert, e := Certificate(id)
	if e != nil {
		raw.Close()
		return nil, e
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, NextProtos: []string{ALPN}, InsecureSkipVerify: true, // PKI names are deliberately replaced by strict, out-of-band device-key pinning below.
		VerifyConnection: func(cs tls.ConnectionState) error {
			return VerifyPeer(cs, func(key string) bool { return key == expectedKey })
		}}
	c := tls.Client(raw, tc)
	_ = c.SetDeadline(time.Now().Add(8 * time.Second))
	if e = c.HandshakeContext(ctx); e != nil {
		c.Close()
		return nil, e
	}
	if _, e = c.Write(preface); e != nil {
		c.Close()
		return nil, e
	}
	var ack [1]byte
	if _, e = io.ReadFull(c, ack[:]); e != nil {
		c.Close()
		return nil, e
	}
	if ack[0] != 0 {
		c.Close()
		return nil, errors.New("remote SSH endpoint unavailable")
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}
func Serve(ctx context.Context, raw net.Conn, id *identity.Identity, allow func(string) bool, sshPort int) error {
	defer raw.Close()
	if sshPort < 1 || sshPort > 65535 {
		return errors.New("invalid local SSH port")
	}
	cert, e := Certificate(id)
	if e != nil {
		return e
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, NextProtos: []string{ALPN}, ClientAuth: tls.RequireAnyClientCert, VerifyConnection: func(cs tls.ConnectionState) error { return VerifyPeer(cs, allow) }}
	c := tls.Server(raw, tc)
	_ = c.SetDeadline(time.Now().Add(8 * time.Second))
	if e = c.HandshakeContext(ctx); e != nil {
		return e
	}
	return serveSSH(ctx, c, sshPort)
}
func serveSSH(ctx context.Context, c net.Conn, sshPort int) error {
	got := make([]byte, len(preface))
	if _, e := io.ReadFull(c, got); e != nil {
		return e
	}
	if !bytes.Equal(got, preface) {
		return errors.New("invalid tunnel preface or unsupported channel")
	}
	endpoint := net.JoinHostPort("127.0.0.1", strconv.Itoa(sshPort))
	local, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", endpoint)
	if e != nil {
		_, _ = c.Write([]byte{1})
		return fmt.Errorf("local SSH connection: %w", e)
	}
	if _, e = c.Write([]byte{0}); e != nil {
		local.Close()
		return e
	}
	_ = c.SetDeadline(time.Time{})
	_, _, e = bridge.Pipe(ctx, c, local, 0)
	return e
}
