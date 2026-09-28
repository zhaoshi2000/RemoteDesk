package filetransfer

import (
 "os"
 "path/filepath"
 "runtime"
 "strings"
 "testing"
)

func TestCanonicalShareRoot(t *testing.T) {
 root := t.TempDir()
 got, err := SafePath(root,"folder/file.txt",true)
 if err != nil { t.Fatal(err) }
 expected, err := filepath.EvalSymlinks(root)
 if err != nil { t.Fatal(err) }
 if got != filepath.Join(expected,"folder","file.txt") { t.Fatalf("unexpected path: %q",got) }
}

func TestRootAndAncestorLinksRejected(t *testing.T) {
 base := t.TempDir()
 actual := filepath.Join(base,"actual")
 if err := os.MkdirAll(filepath.Join(actual,"child"),0700); err != nil { t.Fatal(err) }
 link := filepath.Join(base,"link")
 if err := os.Symlink(actual,link); err != nil { t.Skip("symlink permission unavailable") }
 for _, root := range []string{link,filepath.Join(link,"child")} {
  if _, err := SafePath(root,".",false); err == nil { t.Fatalf("accepted linked root %q",root) }
 }
}

func TestWindowsRootCaseAlias(t *testing.T) {
 if runtime.GOOS != "windows" { t.Skip("Windows case-insensitive path test") }
 root := t.TempDir()
 alias := strings.ToUpper(root)
 a, err := os.Stat(root); if err != nil { t.Fatal(err) }
 b, err := os.Stat(alias)
 if err != nil || !os.SameFile(a,b) { t.Skip("volume uses case-sensitive names") }
 if _, err := SafePath(alias,"file.txt",false); err != nil { t.Fatal(err) }
}
