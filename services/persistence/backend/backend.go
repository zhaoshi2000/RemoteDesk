// Package backend supplies PostgreSQL registry/catalog and Redis TTL/nonce storage.
package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"net"
	"os"
	"remotedesk.local/remotedesk/internal/protocol"
	"time"
)

type Registry struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, dsn string) (*Registry, error) {
	if dsn == "" {
		return nil, errors.New("RD_POSTGRES_URL is required")
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		return nil, errors.New("invalid PostgreSQL configuration")
	}
	ip := net.ParseIP(cfg.ConnConfig.Host)
	local := cfg.ConnConfig.Host == "localhost" || (ip != nil && ip.IsLoopback())
	if !local && cfg.ConnConfig.TLSConfig == nil && os.Getenv("RD_ALLOW_PRIVATE_PLAINTEXT_BACKENDS") != "1" {
		return nil, errors.New("remote PostgreSQL requires TLS; private container networks need explicit opt-in")
	}
	cfg.MaxConns = 12
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		return nil, e
	}
	r := &Registry{pool}
	if e = pool.Ping(ctx); e != nil {
		pool.Close()
		return nil, e
	}
	_, e = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS rd_devices(id TEXT PRIMARY KEY, public_key TEXT NOT NULL, data JSONB NOT NULL); CREATE TABLE IF NOT EXISTS rd_management(id INTEGER PRIMARY KEY CHECK(id=1), data JSONB NOT NULL);`)
	if e != nil {
		pool.Close()
		return nil, e
	}
	return r, nil
}
func (r *Registry) Health(ctx context.Context) error { return r.Pool.Ping(ctx) }
func (r *Registry) Get(id string) (protocol.Device, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var b []byte
	e := r.Pool.QueryRow(ctx, `SELECT data FROM rd_devices WHERE id=$1`, id).Scan(&b)
	var d protocol.Device
	if e != nil {
		return d, false
	}
	if e = json.Unmarshal(b, &d); e != nil {
		slog.Error("invalid registry record")
		return d, false
	}
	return d, true
}
func (r *Registry) List() []protocol.Device {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := []protocol.Device{}
	rows, e := r.Pool.Query(ctx, `SELECT data FROM rd_devices ORDER BY id LIMIT 10000`)
	if e != nil {
		slog.Error("registry unavailable")
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		var d protocol.Device
		if rows.Scan(&b) != nil || json.Unmarshal(b, &d) != nil {
			continue
		}
		out = append(out, d)
	}
	return out
}
func (r *Registry) Register(d protocol.Device) (protocol.Device, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, e := json.Marshal(d)
	if e != nil {
		return d, e
	}
	_, e = r.Pool.Exec(ctx, `INSERT INTO rd_devices(id,public_key,data) VALUES($1,$2,$3) ON CONFLICT(id) DO NOTHING`, d.ID, d.PublicKey, b)
	if e != nil {
		return d, e
	}
	var key string
	var old []byte
	if e = r.Pool.QueryRow(ctx, `SELECT public_key,data FROM rd_devices WHERE id=$1`, d.ID).Scan(&key, &old); e != nil {
		return d, e
	}
	if key != d.PublicKey {
		return d, errors.New("device identity collision")
	}
	e = json.Unmarshal(old, &d)
	return d, e
}
func (r *Registry) Update(d protocol.Device) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, e := json.Marshal(d)
	if e != nil {
		return e
	}
	tag, e := r.Pool.Exec(ctx, `UPDATE rd_devices SET data=$3 WHERE id=$1 AND public_key=$2`, d.ID, d.PublicKey, b)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return errors.New("unknown device or key replacement rejected")
	}
	return nil
}
func (r *Registry) Delete(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tag, e := r.Pool.Exec(ctx, `DELETE FROM rd_devices WHERE id=$1`, id)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return errors.New("unknown device")
	}
	return nil
}
func (r *Registry) Load(ctx context.Context) ([]byte, error) {
	var b []byte
	e := r.Pool.QueryRow(ctx, `SELECT data FROM rd_management WHERE id=1`).Scan(&b)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	return b, e
}
func (r *Registry) Save(ctx context.Context, b []byte) error {
	_, e := r.Pool.Exec(ctx, `INSERT INTO rd_management(id,data) VALUES(1,$1) ON CONFLICT(id) DO UPDATE SET data=EXCLUDED.data`, b)
	return e
}

type Presence struct{ Client *redis.Client }

func OpenRedis(ctx context.Context, address string) (*Presence, error) {
	if address == "" {
		return nil, errors.New("RD_REDIS_URL is required")
	}
	cfg, e := redis.ParseURL(address)
	if e != nil {
		return nil, errors.New("invalid Redis configuration")
	}
	host, _, _ := net.SplitHostPort(cfg.Addr)
	ip := net.ParseIP(host)
	local := host == "localhost" || (ip != nil && ip.IsLoopback())
	if !local && cfg.TLSConfig == nil && os.Getenv("RD_ALLOW_PRIVATE_PLAINTEXT_BACKENDS") != "1" {
		return nil, errors.New("remote Redis requires TLS")
	}
	cfg.DialTimeout = 5 * time.Second
	cfg.ReadTimeout = 2 * time.Second
	cfg.WriteTimeout = 2 * time.Second
	cfg.PoolSize = 12
	c := redis.NewClient(cfg)
	if e = c.Ping(ctx).Err(); e != nil {
		c.Close()
		return nil, e
	}
	return &Presence{c}, nil
}

type online struct {
	Candidates protocol.CandidateSet `json:"candidates"`
	Seen       int64                 `json:"seen"`
}

func (p *Presence) Put(ctx context.Context, id string, c protocol.CandidateSet, seen int64) error {
	b, e := json.Marshal(online{c, seen})
	if e != nil {
		return e
	}
	return p.Client.Set(ctx, "rd:online:"+id, b, 20*time.Second).Err()
}
func (p *Presence) Get(ctx context.Context, id string) (protocol.CandidateSet, int64, bool) {
	b, e := p.Client.Get(ctx, "rd:online:"+id).Bytes()
	if e != nil {
		return protocol.CandidateSet{}, 0, false
	}
	var o online
	if json.Unmarshal(b, &o) != nil {
		return protocol.CandidateSet{}, 0, false
	}
	return o.Candidates, o.Seen, true
}
func (p *Presence) Claim(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	h := sha256.Sum256([]byte(key))
	return p.Client.SetNX(ctx, "rd:nonce:"+hex.EncodeToString(h[:]), "1", ttl).Result()
}
