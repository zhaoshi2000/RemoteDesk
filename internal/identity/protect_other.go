//go:build !windows

package identity

import (
	"bytes"
	"errors"
	"os"
)

func SecureDirectory(dir string) error {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	fi, e := os.Lstat(dir)
	if e != nil {
		return e
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("state must be a real directory")
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return e
	}
	if len(entries) != 0 {
		return errors.New("new identity/server state directory must be empty")
	}
	return os.Chmod(dir, 0700)
}
func protect(b []byte) ([]byte, error) { return append([]byte("RD-UNIX-KEY-v1\n"), b...), nil }
func unprotect(b []byte) ([]byte, error) {
	prefix := []byte("RD-UNIX-KEY-v1\n")
	if !bytes.HasPrefix(b, prefix) {
		return nil, errors.New("unsupported key file or wrong platform")
	}
	return b[len(prefix):], nil
}
