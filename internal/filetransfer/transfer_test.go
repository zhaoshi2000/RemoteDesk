package filetransfer

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPathPolicy(t *testing.T) {
	for _, s := range []string{"../a", "/a", "C:/a", "a\\b", "a//b", "NUL.txt", "com1", "a.", "a ", ".rd-part-hi", "a/../../b", "a:b", "a\nq"} {
		if _, e := CleanPath(s, false); e == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	for _, s := range []string{"文件.txt", "folder/file.bin", "a-b_1.txt"} {
		if _, e := CleanPath(s, false); e != nil {
			t.Fatal(e)
		}
	}
}
func TestRootEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(root, "link")); e != nil {
		t.Skip("symlink permission unavailable")
	}
	if _, e := SafePath(root, "link/no.txt", true); e == nil {
		t.Fatal("followed symlink")
	}
}
func TestFileRoundTrip(t *testing.T) {
	root := t.TempDir()
	local := t.TempDir()
	data := bytes.Repeat([]byte("RemoteDesk 中文\x00"), 3000)
	src := filepath.Join(local, "src")
	os.WriteFile(src, data, 0600)
	dial := func() (net.Conn, <-chan error) {
		a, b := net.Pipe()
		done := make(chan error, 1)
		go func() { done <- Serve(context.Background(), b, root, "peer", 32<<20) }()
		return a, done
	}
	a, d := dial()
	if e := Upload(context.Background(), a, src, "sub/test.bin", false, 32<<20, nil); e != nil {
		t.Fatal(e)
	}
	if e := <-d; e != nil {
		t.Fatal(e)
	}
	a, d = dial()
	dst := filepath.Join(local, "out")
	if e := Download(context.Background(), a, "sub/test.bin", dst, false, 32<<20, nil); e != nil {
		t.Fatal(e)
	}
	if e := <-d; e != nil {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, data) {
		t.Fatal("bytes differ")
	}
	a, d = dial()
	if e := Upload(context.Background(), a, src, "sub/test.bin", false, 32<<20, nil); e == nil {
		t.Fatal("overwrote without confirmation")
	}
	<-d
	a, d = dial()
	rep, e := List(a, "sub")
	a.Close()
	if e != nil || len(rep.Entries) != 1 {
		t.Fatalf("list: %+v %v", rep, e)
	}
	<-d
}
func TestTruncatedControl(t *testing.T) {
	for _, p := range [][]byte{{0, 0, 0, 0}, {0xff, 0xff, 0xff, 0xff}, {0, 0, 0, 5, '{'}} {
		var r Request
		if e := readJSON(bytes.NewReader(p), &r); e == nil {
			t.Fatal("malformed control accepted")
		}
	}
}
