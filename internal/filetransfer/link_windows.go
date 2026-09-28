//go:build windows

package filetransfer

import (
 "os"
 "syscall"
)

func isLinkOrReparse(info os.FileInfo) bool {
 if info.Mode()&os.ModeSymlink != 0 { return true }
 if attrs, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
  return attrs.FileAttributes & syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
 }
 // Fail closed when Windows attributes are unavailable.
 return true
}
