package identity

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func config() Config {
	return Config{Server: "https://localhost:8443", Name: "test", SSHPort: 22, ListenTCP: "127.0.0.1:0", ListenUDP: "127.0.0.1:0"}
}
func TestIdentityRoundTripAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	id, e := Init(dir, config())
	if e != nil {
		t.Fatal(e)
	}
	loaded, c, e := Load(dir)
	if e != nil || !id.Public.Equal(loaded.Public) || c.CAFile != "" {
		t.Fatalf("identity load: %v", e)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "identity.key"))
	if _, e = Init(dir, config()); e == nil {
		t.Fatal("overwrote private identity")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "identity.key"))
	if string(before) != string(after) {
		t.Fatal("key changed")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, "identity.key"))
		if fi.Mode().Perm() != 0600 {
			t.Fatal("private key permissions")
		}
	}
}
func TestExplicitCapabilityACL(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	_, e := Init(a, config())
	if e != nil {
		t.Fatal(e)
	}
	id, e := Init(b, config())
	if e != nil {
		t.Fatal(e)
	}
	var p PublicIdentity
	if e = ReadJSON(filepath.Join(b, "public-identity.json"), &p); e != nil {
		t.Fatal(e)
	}
	if Allowed(a, p.PublicKey, "ssh") {
		t.Fatal("default authorization must deny")
	}
	if e = Trust(a, filepath.Join(b, "public-identity.json"), false, true); e != nil {
		t.Fatal(e)
	}
	if Allowed(a, p.PublicKey, "ssh") || !Allowed(a, p.PublicKey, "probe") {
		t.Fatal("capability isolation failed")
	}
	if e = Untrust(a, id.ID); e != nil {
		t.Fatal(e)
	}
	if Allowed(a, p.PublicKey, "probe") {
		t.Fatal("revocation failed")
	}
}
func TestConfigRejectsInsecureOrigins(t *testing.T) {
	for _, origin := range []string{"http://localhost:8443", "https://user:password@example.com", "https://example.com/path", "https://example.com?token=x"} {
		c := config()
		c.Server = origin
		if ValidateConfig(c) == nil {
			t.Errorf("accepted %s", origin)
		}
	}
}
