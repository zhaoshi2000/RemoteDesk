package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"remotedesk.local/remotedesk/internal/identity"
	"remotedesk.local/remotedesk/internal/protocol"
)

type Config struct {
	Listen          string `json:"listen"`
	STUN            string `json:"stun"`
	TLSCert         string `json:"tls_cert"`
	TLSKey          string `json:"tls_key"`
	Store           string `json:"store"`
	EnrollmentToken string `json:"enrollment_token"`
	AdminToken      string `json:"admin_token"`
	AdminDirectory  string `json:"admin_directory,omitempty"`
	UpdatePublicKey string `json:"update_public_key,omitempty"`
}

func InitConfig(dir, listen, stun, hosts string) (Config, error) {
	var c Config
	abs, e := filepath.Abs(dir)
	if e != nil {
		return c, e
	}
	dir = abs
	if _, e = os.Lstat(filepath.Join(dir, "server.json")); e == nil {
		return c, errors.New("server config exists; refusing overwrite")
	}
	if e = identity.SecureDirectory(dir); e != nil {
		return c, e
	}
	cert, key, e := newCert(strings.Split(hosts, ","))
	if e != nil {
		return c, e
	}
	c = Config{Listen: listen, STUN: stun, TLSCert: filepath.Join(dir, "server.crt"), TLSKey: filepath.Join(dir, "server.key"), Store: filepath.Join(dir, "devices.json"), EnrollmentToken: protocol.RandomHex(32), AdminToken: protocol.RandomHex(32)}
	for p, b := range map[string][]byte{c.TLSCert: cert, c.TLSKey: key, filepath.Join(dir, "enrollment.token"): []byte(c.EnrollmentToken + "\n"), filepath.Join(dir, "admin.token"): []byte(c.AdminToken + "\n")} {
		f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return c, e
		}
		_, e = f.Write(b)
		ce := f.Close()
		if e != nil {
			return c, e
		}
		if ce != nil {
			return c, ce
		}
	}
	e = identity.WriteJSON(filepath.Join(dir, "server.json"), c)
	return c, e
}
func newCert(hosts []string) ([]byte, []byte, error) {
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return nil, nil, e
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return nil, nil, e
	}
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "RemoteDesk private deployment"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(365 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else if h != "" {
			tpl.DNSNames = append(tpl.DNSNames, h)
		}
	}
	if len(tpl.IPAddresses)+len(tpl.DNSNames) == 0 {
		return nil, nil, errors.New("at least one certificate host is required")
	}
	der, e := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if e != nil {
		return nil, nil, e
	}
	raw, e := x509.MarshalPKCS8PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}), e
}
