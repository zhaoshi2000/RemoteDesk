// Package identity owns device identity and local, explicit peer authorization.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"remotedesk.local/remotedesk/internal/protocol"
)

type Config struct {
	DisableTelemetry bool   `json:"disable_telemetry,omitempty"`
	Server           string `json:"server"`
	CAFile           string `json:"ca_file"`
	Name             string `json:"name"`
	STUN             string `json:"stun"`
	ListenTCP        string `json:"listen_tcp"`
	ListenUDP        string `json:"listen_udp"`
	SSHPort          int    `json:"ssh_port"`
	MediaExecutable  string `json:"media_executable,omitempty"`
	MediaRelayUDP    string `json:"media_relay_udp,omitempty"`
	DesktopEnabled   bool   `json:"desktop_enabled,omitempty"`
	ReceiveDirectory string `json:"receive_directory,omitempty"`
}
type Identity struct {
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
	ID      string
}
type PublicIdentity struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
}
type Permission struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	SSH       bool   `json:"ssh"`
	Probe     bool   `json:"probe"`
	Desktop   bool   `json:"desktop"`
	Control   bool   `json:"control"`
	Clipboard bool   `json:"clipboard"`
	Audio     bool   `json:"audio"`
	Files     bool   `json:"files"`
}
type ACL struct {
	Peers map[string]Permission `json:"peers"`
}

func ValidateConfig(c Config) error {
	u, e := url.Parse(c.Server)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("server must be an HTTPS origin without credentials/path/query")
	}
	if c.SSHPort < 1 || c.SSHPort > 65535 {
		return errors.New("invalid SSH port")
	}
	return nil
}
func Init(dir string, c Config) (*Identity, error) {
	if err := ValidateConfig(c); err != nil {
		return nil, err
	}
	if _, e := os.Lstat(filepath.Join(dir, "identity.key")); e == nil {
		return nil, errors.New("identity exists; refusing key overwrite")
	}
	if e := SecureDirectory(dir); e != nil {
		return nil, e
	}
	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return nil, e
	}
	raw, e := x509.MarshalPKCS8PrivateKey(priv)
	if e != nil {
		return nil, e
	}
	sealed, e := protect(raw)
	if e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(dir, "identity.key"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, e
	}
	_, we := f.Write(sealed)
	ce := f.Close()
	if we != nil {
		return nil, we
	}
	if ce != nil {
		return nil, ce
	}
	if e = WriteJSON(filepath.Join(dir, "agent.json"), c); e != nil {
		return nil, e
	}
	if e = WriteJSON(filepath.Join(dir, "trusted.json"), ACL{Peers: map[string]Permission{}}); e != nil {
		return nil, e
	}
	id := &Identity{priv, pub, protocol.DeviceID(pub)}
	if e = WriteJSON(filepath.Join(dir, "public-identity.json"), PublicIdentity{id.ID, c.Name, protocol.PublicString(pub), protocol.Fingerprint(pub)}); e != nil {
		return nil, e
	}
	return id, nil
}
func Load(dir string) (*Identity, Config, error) {
	var c Config
	if e := ReadJSON(filepath.Join(dir, "agent.json"), &c); e != nil {
		return nil, c, e
	}
	if e := ValidateConfig(c); e != nil {
		return nil, c, e
	}
	p := filepath.Join(dir, "identity.key")
	fi, e := os.Lstat(p)
	if e != nil {
		return nil, c, e
	}
	if !fi.Mode().IsRegular() {
		return nil, c, errors.New("identity key is not a regular file")
	}
	raw, e := os.ReadFile(p)
	if e != nil {
		return nil, c, e
	}
	raw, e = unprotect(raw)
	if e != nil {
		return nil, c, e
	}
	key, e := x509.ParsePKCS8PrivateKey(raw)
	if e != nil {
		return nil, c, e
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, c, errors.New("identity key is not Ed25519")
	}
	pub := priv.Public().(ed25519.PublicKey)
	if c.CAFile != "" && !filepath.IsAbs(c.CAFile) {
		c.CAFile = filepath.Join(dir, c.CAFile)
	}
	return &Identity{priv, pub, protocol.DeviceID(pub)}, c, nil
}
func ReadJSON(path string, v any) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	if fi, e := f.Stat(); e != nil || fi.Size() > 1<<20 {
		return errors.New("invalid or oversized JSON file")
	}
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	return d.Decode(v)
}
func WriteJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return WriteAtomic(path, append(b, '\n'), 0600)
}
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, e := os.CreateTemp(dir, ".rd-write-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(mode); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(data); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(tmp, path)
}
func ReadACL(dir string) (ACL, error) {
	a := ACL{Peers: map[string]Permission{}}
	err := ReadJSON(filepath.Join(dir, "trusted.json"), &a)
	if a.Peers == nil {
		a.Peers = map[string]Permission{}
	}
	return a, err
}
func Allowed(dir, key, kind string) bool {
	a, e := ReadACL(dir)
	if e != nil {
		return false
	}
	pub, e := protocol.ParsePublic(key)
	if e != nil {
		return false
	}
	p, ok := a.Peers[protocol.DeviceID(pub)]
	return ok && p.PublicKey == key && ((kind == "ssh" && p.SSH) || (kind == "probe" && p.Probe) || (kind == "desktop" && p.Desktop) || (kind == "file" && p.Files))
}
func Trust(dir, path string, ssh, probe bool) error {
	var p PublicIdentity
	if e := ReadJSON(path, &p); e != nil {
		return e
	}
	pub, e := protocol.ParsePublic(p.PublicKey)
	if e != nil {
		return e
	}
	if p.ID != protocol.DeviceID(pub) || p.Fingerprint != protocol.Fingerprint(pub) {
		return errors.New("public identity ID/fingerprint mismatch")
	}
	a, e := ReadACL(dir)
	if e != nil {
		return e
	}
	if old, ok := a.Peers[p.ID]; ok && old.PublicKey != p.PublicKey {
		return errors.New("refusing trusted key overwrite")
	}
	a.Peers[p.ID] = Permission{Name: p.Name, PublicKey: p.PublicKey, SSH: ssh, Probe: probe}
	return WriteJSON(filepath.Join(dir, "trusted.json"), a)
}
func Untrust(dir, id string) error {
	a, e := ReadACL(dir)
	if e != nil {
		return e
	}
	if _, ok := a.Peers[id]; !ok {
		return fmt.Errorf("peer %s not found", id)
	}
	delete(a.Peers, id)
	return WriteJSON(filepath.Join(dir, "trusted.json"), a)
}

// GrantCapabilities only extends an already fingerprint-verified peer. All grants are opt-in.
func GrantCapabilities(dir, id string, desktop, control, clipboard, audio, files bool) error {
	a, e := ReadACL(dir)
	if e != nil {
		return e
	}
	p, ok := a.Peers[id]
	if !ok {
		return errors.New("trust the peer public identity first")
	}
	p.Desktop = desktop
	p.Control = control
	p.Clipboard = clipboard
	p.Audio = audio
	p.Files = files
	a.Peers[id] = p
	return WriteJSON(filepath.Join(dir, "trusted.json"), a)
}
