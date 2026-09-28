// Package filetransfer provides a bounded, resumable, SHA-256 verified file channel.
// The containing connection MUST already have device-key authenticated TLS.
package filetransfer

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

const MaxFile int64 = 8 << 30
const Chunk = 32 << 10
const maxJSON = 128 << 10

var locks sync.Map

type Request struct {
	Op        string `json:"op"`
	Path      string `json:"path"`
	Size      int64  `json:"size,omitempty"`
	Offset    int64  `json:"offset,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Overwrite bool   `json:"overwrite,omitempty"`
}
type Entry struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Directory bool   `json:"directory"`
	Modified  int64  `json:"modified"`
}
type Reply struct {
	OK      bool    `json:"ok"`
	Error   string  `json:"error,omitempty"`
	Size    int64   `json:"size,omitempty"`
	Offset  int64   `json:"offset,omitempty"`
	SHA256  string  `json:"sha256,omitempty"`
	Entries []Entry `json:"entries,omitempty"`
	More    bool    `json:"more,omitempty"`
}
type Progress struct {
	Path           string `json:"path"`
	Bytes          int64  `json:"bytes"`
	Total          int64  `json:"total"`
	BytesPerSecond int64  `json:"bytes_per_second"`
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func writeJSON(w io.Writer, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(b) > maxJSON {
		return errors.New("file control too large")
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	if e = writeAll(w, h[:]); e != nil {
		return e
	}
	return writeAll(w, b)
}
func readJSON(r io.Reader, v any) error {
	var h [4]byte
	if _, e := io.ReadFull(r, h[:]); e != nil {
		return e
	}
	n := binary.BigEndian.Uint32(h[:])
	if n == 0 || n > maxJSON {
		return errors.New("file control size rejected")
	}
	b := make([]byte, n)
	if _, e := io.ReadFull(r, b); e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func digest(f *os.File) (string, error) {
	if _, e := f.Seek(0, io.SeekStart); e != nil {
		return "", e
	}
	h := sha256.New()
	if _, e := io.Copy(h, io.LimitReader(f, MaxFile+1)); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func validHash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == strings.ToLower(s)
}

// CleanPath is deliberately Windows-strict on every platform. No device names, ADS,
// reserved .rd-part names, control characters, trailing dots/spaces or links are accepted.
func CleanPath(s string, allowRoot bool) (string, error) {
	if (s == "" || s == ".") && allowRoot {
		return ".", nil
	}
	if s == "" || len(s) > 1024 || strings.ContainsAny(s, "\\:\x00") || strings.HasPrefix(s, "/") {
		return "", errors.New("invalid relative file path")
	}
	parts := strings.Split(s, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.TrimRight(p, " .") != p || strings.HasPrefix(strings.ToLower(p), ".rd-") || strings.IndexFunc(p, unicode.IsControl) >= 0 || strings.ContainsAny(p, `<>"|?*`) {
			return "", errors.New("unsafe file path component")
		}
		base := strings.ToUpper(strings.SplitN(p, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9' {
			return "", errors.New("reserved Windows device name")
		}
	}
	return filepath.Join(parts...), nil
}
func SafePath(root, rel string, createParents bool) (string, error) {
	clean, e := CleanPath(rel, true)
	if e != nil {
		return "", e
	}
	root, e = canonicalShareRoot(root)
	if e != nil {
		return "", e
	}
	if clean == "." {
		return root, nil
	}
	cur := root
	parts := strings.Split(clean, string(filepath.Separator))
	for i, p := range parts {
		cur = filepath.Join(cur, p)
		st, e := os.Lstat(cur)
		if os.IsNotExist(e) {
			if i < len(parts)-1 && createParents {
				if e = os.Mkdir(cur, 0700); e != nil {
					return "", e
				}
				continue
			}
			if i == len(parts)-1 {
				return cur, nil
			}
			return "", e
		}
		if e != nil {
			return "", e
		}
		if isLinkOrReparse(st) {
			return "", errors.New("links are not shared")
		}
		if i < len(parts)-1 && !st.IsDir() {
			return "", errors.New("parent is not a directory")
		}
	}
	return cur, nil
}
func limitedCopy(ctx context.Context, c net.Conn, dst io.Writer, src io.Reader, n, offset, rate int64, path string, progress func(Progress)) error {
	if rate < 64<<10 || rate > 32<<20 {
		return errors.New("file rate must be between 64 KiB/s and 32 MiB/s")
	}
	started := time.Now()
	sent := int64(0)
	buf := make([]byte, Chunk)
	last := time.Time{}
	for sent < n {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		want := int64(len(buf))
		if n-sent < want {
			want = n - sent
		}
		_ = c.SetDeadline(time.Now().Add(30 * time.Second))
		got, e := io.ReadFull(src, buf[:want])
		if got > 0 {
			if we := writeAll(dst, buf[:got]); we != nil {
				return we
			}
			sent += int64(got)
		}
		if e != nil {
			return e
		}
		expected := time.Duration(sent) * time.Second / time.Duration(rate)
		if pause := expected - time.Since(started); pause > 0 {
			t := time.NewTimer(pause)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
		if progress != nil && time.Since(last) > 200*time.Millisecond {
			secs := time.Since(started).Seconds()
			progress(Progress{path, offset + sent, offset + n, int64(float64(sent) / secs)})
			last = time.Now()
		}
	}
	if progress != nil {
		progress(Progress{path, offset + n, offset + n, int64(float64(sent) / max(time.Since(started).Seconds(), 0.001))})
	}
	return nil
}

// Serve permits one request per TLS session. The root must be private to the host
// user; an untrusted local user allowed to replace its parents can defeat pathname checks.
func Serve(ctx context.Context, c net.Conn, root, peer string, rate int64) error {
	defer c.Close()
	if root == "" {
		return errors.New("file sharing disabled")
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if _, busy := locks.LoadOrStore(root, true); busy {
		return errors.New("a file transfer is already active for this share")
	}
	defer locks.Delete(root)
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	var q Request
	if e := readJSON(c, &q); e != nil {
		return e
	}
	fail := func(e error) error { _ = writeJSON(c, Reply{Error: e.Error()}); return e }
	if q.Op != "list" && q.Op != "get" && q.Op != "put" {
		return fail(errors.New("unknown file operation"))
	}
	path, e := SafePath(root, q.Path, q.Op == "put")
	if e != nil {
		return fail(e)
	}
	switch q.Op {
	case "list":
		f, e := os.Open(path)
		if e != nil {
			return fail(e)
		}
		defer f.Close()
		items, e := f.ReadDir(501)
		if e != nil && e != io.EOF {
			return fail(e)
		}
		rep := Reply{OK: true, Entries: []Entry{}, More: len(items) > 500}
		if len(items) > 500 {
			items = items[:500]
		}
		for _, d := range items {
			if d.Type()&os.ModeSymlink != 0 || strings.HasPrefix(d.Name(), ".rd-") {
				continue
			}
			info, e := d.Info()
			if e != nil {
				continue
			}
			rep.Entries = append(rep.Entries, Entry{d.Name(), info.Size(), d.IsDir(), info.ModTime().Unix()})
		}
		return writeJSON(c, rep)
	case "get":
		f, e := os.Open(path)
		if e != nil {
			return fail(e)
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() || st.Size() > MaxFile {
			return fail(errors.New("not a bounded regular file"))
		}
		if q.Offset < 0 || q.Offset > st.Size() {
			return fail(errors.New("invalid resume offset"))
		}
		hash, e := digest(f)
		if e != nil {
			return fail(e)
		}
		if q.SHA256 != "" && hash != q.SHA256 {
			return fail(errors.New("remote file changed; restart download"))
		}
		if e = writeJSON(c, Reply{OK: true, Size: st.Size(), Offset: q.Offset, SHA256: hash}); e != nil {
			return e
		}
		if _, e = f.Seek(q.Offset, io.SeekStart); e != nil {
			return e
		}
		return limitedCopy(ctx, c, c, f, st.Size()-q.Offset, q.Offset, rate, q.Path, nil)
	case "put":
		if q.Size < 0 || q.Size > MaxFile || !validHash(q.SHA256) {
			return fail(errors.New("invalid file size or digest"))
		}
		if q.Path == "." || q.Path == "" {
			return fail(errors.New("upload requires a file name"))
		}
		if st, e := os.Lstat(path); e == nil {
			if !st.Mode().IsRegular() {
				return fail(errors.New("destination is not a regular file"))
			}
			if !q.Overwrite {
				return fail(errors.New("destination exists; explicit overwrite confirmation required"))
			}
		} else if !os.IsNotExist(e) {
			return fail(e)
		}
		tag := sha256.Sum256([]byte(peer + "\x00" + q.Path + "\x00" + q.SHA256))
		partial := filepath.Join(filepath.Dir(path), fmt.Sprintf(".rd-part-%x", tag[:16]))
		if st, e := os.Lstat(partial); e == nil && !st.Mode().IsRegular() {
			return fail(errors.New("unsafe partial file"))
		}
		f, e := os.OpenFile(partial, os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return fail(e)
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil {
			return fail(e)
		}
		offset := st.Size()
		if offset > q.Size {
			return fail(errors.New("resume file is larger than source"))
		}
		if _, e = f.Seek(offset, io.SeekStart); e != nil {
			return fail(e)
		}
		if e = writeJSON(c, Reply{OK: true, Offset: offset, Size: q.Size, SHA256: q.SHA256}); e != nil {
			return e
		}
		if e = limitedCopy(ctx, c, f, c, q.Size-offset, offset, rate, q.Path, nil); e != nil {
			return e
		}
		if e = f.Sync(); e != nil {
			return fail(e)
		}
		hash, e := digest(f)
		if e != nil {
			return fail(e)
		}
		if hash != q.SHA256 {
			f.Close()
			_ = os.Remove(partial)
			return fail(errors.New("SHA-256 mismatch; partial file discarded"))
		}
		if e = f.Close(); e != nil {
			return fail(e)
		}
		// Re-check before commit; no symlink destinations or changed parent traversal.
		if _, e = SafePath(root, q.Path, false); e != nil {
			return fail(e)
		}
		if !q.Overwrite {
			if _, e = os.Lstat(path); e == nil {
				return fail(errors.New("destination appeared during upload"))
			}
		}
		if e = os.Rename(partial, path); e != nil {
			return fail(e)
		}
		return writeJSON(c, Reply{OK: true, Size: q.Size, SHA256: hash})
	}
	return errors.New("unhandled file request")
}
func List(c net.Conn, path string) (Reply, error) {
	if e := writeJSON(c, Request{Op: "list", Path: path}); e != nil {
		return Reply{}, e
	}
	var r Reply
	e := readJSON(c, &r)
	if e == nil && !r.OK {
		e = errors.New(r.Error)
	}
	return r, e
}
func Upload(ctx context.Context, c net.Conn, local, remote string, overwrite bool, rate int64, progress func(Progress)) error {
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	st, e := os.Lstat(local)
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() || st.Size() > MaxFile {
		return errors.New("source is not a bounded regular file")
	}
	f, e := os.Open(local)
	if e != nil {
		return e
	}
	defer f.Close()
	hash, e := digest(f)
	if e != nil {
		return e
	}
	if _, e = CleanPath(remote, false); e != nil {
		return e
	}
	if e = writeJSON(c, Request{Op: "put", Path: remote, Size: st.Size(), SHA256: hash, Overwrite: overwrite}); e != nil {
		return e
	}
	var rep Reply
	if e = readJSON(c, &rep); e != nil {
		return e
	}
	if !rep.OK {
		return errors.New(rep.Error)
	}
	if rep.Offset < 0 || rep.Offset > st.Size() || rep.SHA256 != hash {
		return errors.New("invalid resume response")
	}
	if _, e = f.Seek(rep.Offset, io.SeekStart); e != nil {
		return e
	}
	if e = limitedCopy(ctx, c, c, f, st.Size()-rep.Offset, rep.Offset, rate, remote, progress); e != nil {
		return e
	}
	if e = readJSON(c, &rep); e != nil {
		return e
	}
	if !rep.OK || rep.SHA256 != hash {
		return fmt.Errorf("upload not committed: %s", rep.Error)
	}
	return nil
}
func Download(ctx context.Context, c net.Conn, remote, local string, overwrite bool, rate int64, progress func(Progress)) error {
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if _, e := CleanPath(remote, false); e != nil {
		return e
	}
	if st, e := os.Lstat(local); e == nil {
		if !st.Mode().IsRegular() {
			return errors.New("unsafe local target")
		}
		if !overwrite {
			return errors.New("local file exists; overwrite confirmation required")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	partial := local + ".rd-download"
	metadata := partial + ".json"
	for _, path := range []string{partial, metadata} {
		if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("resume files must be regular, non-symlink files")
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	var old Reply
	if b, e := os.ReadFile(metadata); e == nil {
		if e = json.Unmarshal(b, &old); e != nil {
			return e
		}
	}
	var offset int64
	if st, e := os.Lstat(partial); e == nil {
		if !st.Mode().IsRegular() || !validHash(old.SHA256) {
			return errors.New("unsafe/unidentified local partial file")
		}
		offset = st.Size()
	}
	if e := writeJSON(c, Request{Op: "get", Path: remote, Offset: offset, SHA256: old.SHA256}); e != nil {
		return e
	}
	var rep Reply
	if e := readJSON(c, &rep); e != nil {
		return e
	}
	if !rep.OK {
		return errors.New(rep.Error)
	}
	if rep.Size < offset || rep.Size > MaxFile || rep.Offset != offset || !validHash(rep.SHA256) {
		return errors.New("invalid file response")
	}
	b, _ := json.Marshal(rep)
	if e := os.WriteFile(metadata, b, 0600); e != nil {
		return e
	}
	f, e := os.OpenFile(partial, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = f.Seek(offset, io.SeekStart); e != nil {
		return e
	}
	if e = limitedCopy(ctx, c, f, c, rep.Size-offset, offset, rate, remote, progress); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	hash, e := digest(f)
	if e != nil {
		return e
	}
	if hash != rep.SHA256 {
		f.Close()
		_ = os.Remove(partial)
		_ = os.Remove(metadata)
		return errors.New("download SHA-256 mismatch")
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(partial, local); e != nil {
		return e
	}
	_ = os.Remove(metadata)
	return nil
}
