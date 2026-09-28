// Package update authenticates and stages versioned release bundles. It never
// executes a downloaded installer, disables signature validation, or stores a signing key.
package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"remotedesk.local/remotedesk/internal/filetransfer"
	"runtime"
	"strings"
	"time"
)

const MaxArchive int64 = 1 << 30
const MaxExtracted uint64 = 2 << 30
const Domain = "RemoteDesk signed release v1\n"

type Manifest struct {
	Product   string `json:"product"`
	Version   string `json:"version"`
	Sequence  uint64 `json:"sequence"`
	Platform  string `json:"platform"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at"`
}
type Envelope struct {
	Manifest  Manifest `json:"manifest"`
	Signature string   `json:"signature"`
}

func Canonical(m Manifest) []byte { b, _ := json.Marshal(m); return append([]byte(Domain), b...) }
func Sign(m Manifest, key ed25519.PrivateKey) Envelope {
	return Envelope{m, base64.StdEncoding.EncodeToString(ed25519.Sign(key, Canonical(m)))}
}
func ParsePublic(s string) (ed25519.PublicKey, error) {
	p, e := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if e != nil || len(p) != ed25519.PublicKeySize {
		return nil, errors.New("invalid pinned Ed25519 update key")
	}
	return ed25519.PublicKey(p), nil
}
func Verify(b []byte, key ed25519.PublicKey, minSequence uint64, platform string, now time.Time) (Manifest, error) {
	var env Envelope
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(&env); e != nil {
		return Manifest{}, e
	}
	var more any
	if e := d.Decode(&more); e != io.EOF {
		return Manifest{}, errors.New("trailing signed manifest data")
	}
	m := env.Manifest
	sig, e := base64.StdEncoding.DecodeString(env.Signature)
	if e != nil || len(key) != 32 || !ed25519.Verify(key, Canonical(m), sig) {
		return m, errors.New("release signature mismatch")
	}
	if m.Product != "RemoteDesk" || m.Sequence <= minSequence || m.Platform != platform || len(m.Version) == 0 || len(m.Version) > 64 || strings.ContainsAny(m.Version, "/\\:\x00") || m.Size < 1 || m.Size > MaxArchive {
		return m, errors.New("wrong release product/platform, downgrade or invalid bounds")
	}
	h, e := hex.DecodeString(m.SHA256)
	if e != nil || len(h) != 32 {
		return m, errors.New("invalid release digest")
	}
	if m.IssuedAt > now.Add(5*time.Minute).Unix() || m.ExpiresAt <= now.Unix() || m.ExpiresAt <= m.IssuedAt || m.ExpiresAt-m.IssuedAt > 90*86400 {
		return m, errors.New("expired or invalid release validity window")
	}
	u, e := url.Parse(m.URL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return m, errors.New("release URL must use HTTPS without credentials")
	}
	return m, nil
}
func Client() *http.Client {
	return &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if r.URL.Scheme != "https" || r.URL.User != nil {
			return errors.New("unsafe update redirect")
		}
		return nil
	}}
}
func FetchManifest(ctx context.Context, c *http.Client, address string) ([]byte, error) {
	u, e := url.Parse(address)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Host == "" {
		return nil, errors.New("manifest must use HTTPS")
	}
	q, e := http.NewRequestWithContext(ctx, "GET", address, nil)
	if e != nil {
		return nil, e
	}
	resp, e := c.Do(q)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("manifest HTTP %d", resp.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if len(b) > 65536 {
		return nil, errors.New("manifest too large")
	}
	return b, e
}
func Extract(archive, dest string) error {
	z, e := zip.OpenReader(archive)
	if e != nil {
		return e
	}
	defer z.Close()
	if len(z.File) > 10000 {
		return errors.New("too many release files")
	}
	if e = os.Mkdir(dest, 0700); e != nil {
		return e
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(dest)
		}
	}()
	seen := map[string]bool{}
	var total uint64
	for _, f := range z.File {
		name := strings.TrimSuffix(f.Name, "/")
		p, e := filetransfer.CleanPath(name, false)
		if e != nil {
			return e
		}
		lower := strings.ToLower(filepath.ToSlash(p))
		if seen[lower] {
			return errors.New("duplicate/case-colliding archive path")
		}
		seen[lower] = true
		if f.Mode()&os.ModeSymlink != 0 || !f.Mode().IsRegular() && !f.FileInfo().IsDir() {
			return errors.New("release links/special files forbidden")
		}
		if f.UncompressedSize64 > 512<<20 || total+f.UncompressedSize64 > MaxExtracted {
			return errors.New("release expansion limit")
		}
		total += f.UncompressedSize64
		out := filepath.Join(dest, p)
		if f.FileInfo().IsDir() {
			if e = os.MkdirAll(out, 0700); e != nil {
				return e
			}
			continue
		}
		if e = os.MkdirAll(filepath.Dir(out), 0700); e != nil {
			return e
		}
		src, e := f.Open()
		if e != nil {
			return e
		}
		dst, e := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			src.Close()
			return e
		}
		n, ce := io.Copy(dst, io.LimitReader(src, int64(f.UncompressedSize64)+1))
		syncErr := dst.Sync()
		closeErr := dst.Close()
		srcErr := src.Close()
		if ce != nil {
			return ce
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		if srcErr != nil {
			return srcErr
		}
		if uint64(n) != f.UncompressedSize64 {
			return errors.New("archive length mismatch")
		}
		if runtime.GOOS != "windows" && f.Mode()&0111 != 0 {
			if e = os.Chmod(out, 0700); e != nil {
				return e
			}
		}
	}
	success = true
	return nil
}
func Stage(ctx context.Context, c *http.Client, m Manifest, root string) (string, error) {
	if e := os.MkdirAll(root, 0700); e != nil {
		return "", e
	}
	f, e := os.CreateTemp(root, ".release-*.zip")
	if e != nil {
		return "", e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	q, e := http.NewRequestWithContext(ctx, "GET", m.URL, nil)
	if e != nil {
		return "", e
	}
	resp, e := c.Do(q)
	if e != nil {
		return "", e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("release HTTP %d", resp.StatusCode)
	}
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, m.Size+1))
	if e != nil {
		return "", e
	}
	if n != m.Size || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), m.SHA256) {
		return "", errors.New("release size/SHA-256 mismatch")
	}
	if e = f.Sync(); e != nil {
		return "", e
	}
	if e = f.Close(); e != nil {
		return "", e
	}
	dest := filepath.Join(root, fmt.Sprintf("release-%020d", m.Sequence))
	if e = Extract(tmp, dest); e != nil {
		return "", e
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if e = os.WriteFile(filepath.Join(dest, "release-receipt.json"), b, 0600); e != nil {
		os.RemoveAll(dest)
		return "", e
	}
	return dest, nil
}
