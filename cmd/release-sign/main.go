// Offline release signing utility. Keep the private key outside source control.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"remotedesk.local/remotedesk/internal/update"
	"strings"
	"time"
)

func run() error {
	f := flag.NewFlagSet("release-sign", flag.ContinueOnError)
	keygen := f.Bool("keygen", false, "generate a new offline key; never overwrite")
	keypath := f.String("key", "release-private.key", "private Ed25519 key file")
	bundle := f.String("bundle", "", "ZIP bundle")
	address := f.String("url", "", "HTTPS bundle URL")
	version := f.String("version", "", "version")
	sequence := f.Uint64("sequence", 0, "strictly increasing release sequence")
	platform := f.String("platform", "windows-amd64", "release platform")
	out := f.String("out", "release.json", "manifest output")
	if e := f.Parse(os.Args[1:]); e != nil {
		return e
	}
	if *keygen {
		pub, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return e
		}
		fd, e := os.OpenFile(*keypath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = fd.WriteString(base64.StdEncoding.EncodeToString(key) + "\n")
		ce := fd.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		fmt.Println("Public key:", base64.StdEncoding.EncodeToString(pub))
		return nil
	}
	raw, e := os.ReadFile(*keypath)
	if e != nil {
		return e
	}
	key, e := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if e != nil || len(key) != 64 {
		return errors.New("invalid signing key")
	}
	file, e := os.Open(*bundle)
	if e != nil {
		return e
	}
	defer file.Close()
	h := sha256.New()
	size, e := io.Copy(h, io.LimitReader(file, update.MaxArchive+1))
	if e != nil {
		return e
	}
	now := time.Now()
	m := update.Manifest{Product: "RemoteDesk", Version: *version, Sequence: *sequence, Platform: *platform, URL: *address, SHA256: hex.EncodeToString(h.Sum(nil)), Size: size, IssuedAt: now.Unix(), ExpiresAt: now.Add(30 * 24 * time.Hour).Unix()}
	b, e := json.MarshalIndent(update.Sign(m, key), "", "  ")
	if e != nil {
		return e
	}
	if _, e = update.Verify(b, ed25519.PrivateKey(key).Public().(ed25519.PublicKey), 0, *platform, now); e != nil {
		return e
	}
	fout, e := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = fout.Write(b)
	ce := fout.Close()
	if e != nil {
		return e
	}
	return ce
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
