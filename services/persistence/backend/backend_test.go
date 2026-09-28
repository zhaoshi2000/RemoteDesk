package backend

import (
	"context"
	"os"
	"remotedesk.local/remotedesk/internal/protocol"
	"testing"
	"time"
)

func TestPostgresRedisIntegration(t *testing.T) {
	dsn := os.Getenv("RD_POSTGRES_URL")
	if dsn == "" {
		t.Skip("RD_POSTGRES_URL/RD_REDIS_URL not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, e := Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Pool.Close()
	id := "test-" + protocol.RandomHex(8)
	d := protocol.Device{ID: id, Name: "integration", PublicKey: "test-key"}
	if _, e = r.Register(d); e != nil {
		t.Fatal(e)
	}
	defer r.Delete(id)
	d.Name = "renamed"
	if e = r.Update(d); e != nil {
		t.Fatal(e)
	}
	got, ok := r.Get(id)
	if !ok || got.Name != "renamed" {
		t.Fatal("registry update not persisted")
	}
	d.PublicKey = "wrong"
	if e = r.Update(d); e == nil {
		t.Fatal("key replacement accepted")
	}
	p, e := OpenRedis(ctx, os.Getenv("RD_REDIS_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer p.Client.Close()
	if e = p.Put(ctx, id, protocol.CandidateSet{DeviceID: id}, time.Now().Unix()); e != nil {
		t.Fatal(e)
	}
	if c, _, ok := p.Get(ctx, id); !ok || c.DeviceID != id {
		t.Fatal("online TTL record missing")
	}
	nonce := "test-" + protocol.RandomHex(16)
	if ok, e := p.Claim(ctx, nonce, time.Minute); e != nil || !ok {
		t.Fatal("nonce claim failed", e)
	}
	if ok, e := p.Claim(ctx, nonce, time.Minute); e != nil || ok {
		t.Fatal("nonce replay accepted", e)
	}
}
