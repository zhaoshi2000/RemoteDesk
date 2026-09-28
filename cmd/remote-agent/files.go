package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"remotedesk.local/remotedesk/internal/agent"
	"remotedesk.local/remotedesk/internal/apiclient"
	"remotedesk.local/remotedesk/internal/filetransfer"
	"remotedesk.local/remotedesk/internal/identity"
	"strings"
)

func fileCommand(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("file", flag.ContinueOnError)
	state := f.String("state", "state/agent", "private device state")
	peer := f.String("peer", "", "remote device ID")
	op := f.String("op", "list", "list, get, put or put-folder")
	remote := f.String("remote", ".", "path relative to configured share")
	local := f.String("local", "", "local file or folder")
	overwrite := f.Bool("overwrite", false, "explicit overwrite confirmation")
	rate := f.Int64("rate", 1<<20, "file bytes/sec (64KiB..32MiB)")
	relay := f.Bool("relay-only", false, "force relay")
	if e := f.Parse(args); e != nil {
		return e
	}
	if *peer == "" {
		return errors.New("--peer required")
	}
	if *rate < 64<<10 || *rate > 32<<20 {
		return errors.New("invalid file rate")
	}
	progress := func(p filetransfer.Progress) { _ = json.NewEncoder(os.Stdout).Encode(p) }
	run := func(op, src, dst string) error {
		c, mode, e := agent.DialFile(ctx, *state, *peer, *relay)
		if e != nil {
			return e
		}
		defer c.Close()
		fmt.Fprintln(os.Stderr, "File channel:", mode)
		switch op {
		case "list":
			rep, e := filetransfer.List(c, dst)
			if e != nil {
				return e
			}
			return printJSON(rep)
		case "put":
			return filetransfer.Upload(ctx, c, src, dst, *overwrite, *rate, progress)
		case "get":
			return filetransfer.Download(ctx, c, dst, src, *overwrite, *rate, progress)
		}
		return errors.New("unknown file operation")
	}
	if *op != "put-folder" {
		return run(*op, *local, *remote)
	}
	if *local == "" {
		return errors.New("--local folder required")
	}
	root, e := filepath.Abs(*local)
	if e != nil {
		return e
	}
	return filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("folder upload refuses links")
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		return run("put", p, path.Join(*remote, filepath.ToSlash(rel)))
	})
}
func configureHost(args []string) error {
	f := flag.NewFlagSet("configure-host", flag.ContinueOnError)
	state := f.String("state", "state/agent", "private device state")
	desktop := f.Bool("desktop", false, "explicitly enable desktop hosting")
	engine := f.String("engine", "", "absolute path to remote-media.exe")
	receive := f.String("receive-dir", "", "private shared file root; empty disables file sharing")
	if e := f.Parse(args); e != nil {
		return e
	}
	_, cfg, e := identity.Load(*state)
	if e != nil {
		return e
	}
	if *desktop {
		if *engine == "" {
			return errors.New("--engine required")
		}
		p, e := filepath.Abs(*engine)
		if e != nil {
			return e
		}
		st, e := os.Lstat(p)
		if e != nil || !st.Mode().IsRegular() {
			return errors.New("media engine is not a regular executable")
		}
		cfg.MediaExecutable = p
	}
	cfg.DesktopEnabled = *desktop
	cfg.ReceiveDirectory = ""
	if *receive != "" {
		p, e := filepath.Abs(*receive)
		if e != nil {
			return e
		}
		if p == filepath.VolumeName(p)+string(filepath.Separator) {
			return errors.New("cannot share an entire volume")
		}
		if strings.EqualFold(filepath.Base(p), "Windows") {
			return errors.New("cannot share Windows system directory")
		}
		if e = identity.SecureDirectory(p); e != nil {
			return e
		}
		if _, e = filetransfer.SafePath(p, ".", false); e != nil {
			return e
		}
		cfg.ReceiveDirectory = p
	}
	if e = identity.WriteJSON(filepath.Join(*state, "agent.json"), cfg); e != nil {
		return e
	}
	fmt.Println("Local hosting configuration saved. Restart the agent to apply. Peer capability grants remain separate.")
	return nil
}
func listPeers(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("peers", flag.ContinueOnError)
	state := f.String("state", "state/agent", "private state")
	if e := f.Parse(args); e != nil {
		return e
	}
	id, cfg, e := identity.Load(*state)
	if e != nil {
		return e
	}
	acl, e := identity.ReadACL(*state)
	if e != nil {
		return e
	}
	api, e := apiclient.New(id, cfg)
	if e != nil {
		return e
	}
	defer api.Close()
	out := []map[string]any{}
	for key, p := range acl.Peers {
		item := map[string]any{"id": key, "name": p.Name, "permissions": p}
		peer, e := api.Peer(ctx, key)
		if e == nil {
			item["online"] = peer.Online
			item["last_seen"] = peer.LastSeen
		} else {
			item["online"] = false
			item["error"] = e.Error()
		}
		out = append(out, item)
	}
	return printJSON(out)
}
