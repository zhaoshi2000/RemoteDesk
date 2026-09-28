package filetransfer

import (
 "errors"
 "os"
 "path/filepath"
)

// Check the original root before canonicalization. Windows may expand an 8.3
// alias or change casing without following a link. Keep the shared tree private
// to the local administrator; these pathname checks do not prevent local races.
func canonicalShareRoot(root string) (string, error) {
 absolute, err := filepath.Abs(root)
 if err != nil { return "", err }
 for cur := absolute; ; cur = filepath.Dir(cur) {
  info, err := os.Lstat(cur)
  if err != nil { return "", err }
  if isLinkOrReparse(info) { return "", errors.New("share root must not traverse links or reparse points") }
  if !info.IsDir() { return "", errors.New("share root ancestor is not a directory") }
  if filepath.Dir(cur) == cur { break }
 }
 return filepath.EvalSymlinks(absolute)
}
