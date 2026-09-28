//go:build windows

package identity

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

type blob struct {
	Size uint32
	Data *byte
}

var crypt32 = syscall.NewLazyDLL("crypt32.dll")
var protectProc = crypt32.NewProc("CryptProtectData")
var unprotectProc = crypt32.NewProc("CryptUnprotectData")
var localFree = syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree")

func SecureDirectory(dir string) error {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	fi, e := os.Lstat(dir)
	if e != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("state must be a real directory")
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return e
	}
	if len(entries) != 0 {
		return errors.New("new identity/server state directory must be empty")
	}
	var tok syscall.Token
	process, e := syscall.GetCurrentProcess()
	if e != nil {
		return e
	}
	if e = syscall.OpenProcessToken(process, syscall.TOKEN_QUERY, &tok); e != nil {
		return e
	}
	defer tok.Close()
	u, e := tok.GetTokenUser()
	if e != nil {
		return e
	}
	sid, e := u.User.Sid.String()
	if e != nil {
		return e
	}
	// Install an exact protected DACL, rather than retaining an inherited or explicit Everyone ACE.
	advapi := syscall.NewLazyDLL("advapi32.dll")
	convert := advapi.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	getDACL := advapi.NewProc("GetSecurityDescriptorDacl")
	setNamed := advapi.NewProc("SetNamedSecurityInfoW")
	sddl, e := syscall.UTF16PtrFromString("D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if e != nil {
		return e
	}
	var descriptor uintptr
	r, _, callErr := convert.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&descriptor)), 0)
	if r == 0 {
		return fmt.Errorf("create state DACL: %w", callErr)
	}
	defer localFree.Call(descriptor)
	var present, defaulted uint32
	var dacl uintptr
	r, _, callErr = getDACL.Call(descriptor, uintptr(unsafe.Pointer(&present)), uintptr(unsafe.Pointer(&dacl)), uintptr(unsafe.Pointer(&defaulted)))
	if r == 0 || present == 0 || dacl == 0 {
		return fmt.Errorf("read state DACL: %w", callErr)
	}
	path, e := syscall.UTF16PtrFromString(dir)
	if e != nil {
		return e
	}
	result, _, _ := setNamed.Call(uintptr(unsafe.Pointer(path)), 1, 0x80000004, 0, 0, dacl, 0)
	if result != 0 {
		return fmt.Errorf("protect state directory DACL: %w", syscall.Errno(result))
	}
	return nil
}
func crypt(b []byte, encrypt bool) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("empty DPAPI input")
	}
	in := blob{uint32(len(b)), &b[0]}
	var out blob
	var r uintptr
	var e error
	if encrypt {
		r, _, e = protectProc.Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 5, uintptr(unsafe.Pointer(&out)))
	} else {
		r, _, e = unprotectProc.Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&out)))
	}
	if r == 0 {
		return nil, fmt.Errorf("DPAPI: %w", e)
	}
	defer localFree.Call(uintptr(unsafe.Pointer(out.Data)))
	result := append([]byte{}, unsafe.Slice(out.Data, int(out.Size))...)
	return result, nil
}
func protect(b []byte) ([]byte, error) {
	v, e := crypt(b, true)
	if e != nil {
		return nil, e
	}
	return append([]byte("RD-DPAPI-MACHINE-v1\n"), v...), nil
}
func unprotect(b []byte) ([]byte, error) {
	p := []byte("RD-DPAPI-MACHINE-v1\n")
	if !bytes.HasPrefix(b, p) {
		return nil, errors.New("invalid Windows key file")
	}
	return crypt(b[len(p):], false)
}
