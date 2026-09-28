// remote-updater verifies and stages signed releases. Activation is intentionally
// explicit: never replace a running service or execute an unverified installer.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"remotedesk.local/remotedesk/internal/update"
	"runtime"
	"time"
)

func run() error {
	f := flag.NewFlagSet("remote-updater", flag.ContinueOnError)
	address := f.String("manifest", "", "HTTPS signed manifest URL")
	keyPath := f.String("public-key", "", "pinned release Ed25519 public key file")
	minimum := f.Uint64("minimum-sequence", 0, "last installed sequence, reject older/equal releases")
	root := f.String("staging", "updates", "private staging root")
	verifyOnly := f.Bool("verify-only", false, "verify without downloading bundle")
	if e := f.Parse(os.Args[1:]); e != nil {
		return e
	}
	if *address == "" || *keyPath == "" {
		return errors.New("--manifest and --public-key are required")
	}
	raw, e := os.ReadFile(*keyPath)
	if e != nil {
		return e
	}
	pub, e := update.ParsePublic(string(raw))
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	c := update.Client()
	b, e := update.FetchManifest(ctx, c, *address)
	if e != nil {
		return e
	}
	m, e := update.Verify(b, pub, *minimum, runtime.GOOS+"-"+runtime.GOARCH, time.Now())
	if e != nil {
		return e
	}
	if *verifyOnly {
		return json.NewEncoder(os.Stdout).Encode(m)
	}
	dest, e := update.Stage(ctx, c, m, *root)
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"verified": true, "staged": dest, "sequence": m.Sequence, "activated": false})
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "update rejected:", e)
		os.Exit(1)
	}
}
