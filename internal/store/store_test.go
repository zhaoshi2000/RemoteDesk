package store

import (
	"os"
	"path/filepath"
	"remotedesk.local/remotedesk/internal/protocol"
	"testing"
)

func TestDurableRegistrationAndCollisionGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	d := protocol.Device{ID: "123", Name: "PC", PublicKey: "key-one", RegisteredAt: 42}
	if _, e = s.Register(d); e != nil {
		t.Fatal(e)
	}
	loaded, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	got, ok := loaded.Get(d.ID)
	if !ok || got != d {
		t.Fatal("registration did not persist")
	}
	d.PublicKey = "key-two"
	if _, e = loaded.Register(d); e == nil {
		t.Fatal("ID collision overwrote key")
	}
}
func TestCorruptStoreFailsClosed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "devices.json")
	if e := os.WriteFile(p, []byte("{broken"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(p); e == nil {
		t.Fatal("corrupt store silently replaced")
	}
}
