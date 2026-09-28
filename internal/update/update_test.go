package update

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSignedManifest(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	m := Manifest{"RemoteDesk", "0.2.0", 2, "windows-amd64", "https://example.test/release.zip", string(bytes.Repeat([]byte("a"), 64)), 42, now.Unix(), now.Add(time.Hour).Unix()}
	b, _ := json.Marshal(Sign(m, key))
	if _, e := Verify(b, pub, 1, "windows-amd64", now); e != nil {
		t.Fatal(e)
	}
	if _, e := Verify(b, pub, 2, "windows-amd64", now); e == nil {
		t.Fatal("downgrade accepted")
	}
	if _, e := Verify(b, pub, 0, "linux-amd64", now); e == nil {
		t.Fatal("wrong platform accepted")
	}
	if _, e := Verify(b, pub, 0, "windows-amd64", now.Add(2*time.Hour)); e == nil {
		t.Fatal("expiry ignored")
	}
	b = bytes.Replace(b, []byte("0.2.0"), []byte("0.2.1"), 1)
	if _, e := Verify(b, pub, 0, "windows-amd64", now); e == nil {
		t.Fatal("tamper accepted")
	}
}
func archive(t *testing.T, names []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.zip")
	f, _ := os.Create(p)
	z := zip.NewWriter(f)
	for _, n := range names {
		w, e := z.Create(n)
		if e != nil {
			t.Fatal(e)
		}
		w.Write([]byte("payload"))
	}
	z.Close()
	f.Close()
	return p
}
func TestArchivePolicy(t *testing.T) {
	for _, names := range [][]string{{"../escape"}, {"C:/escape"}, {"a", "A"}, {"NUL.exe"}, {"dir/../../x"}} {
		dst := filepath.Join(t.TempDir(), "stage")
		if e := Extract(archive(t, names), dst); e == nil {
			t.Fatalf("accepted %v", names)
		}
		if _, e := os.Stat(dst); !os.IsNotExist(e) {
			t.Fatal("failed staging was not cleaned")
		}
	}
	dst := filepath.Join(t.TempDir(), "stage")
	if e := Extract(archive(t, []string{"remote-agent.exe", "plugins/test.dll"}), dst); e != nil {
		t.Fatal(e)
	}
}
