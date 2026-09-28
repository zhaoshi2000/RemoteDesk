//go:build windows

package media

import (
	"os/exec"
	"syscall"
	"time"
	"unsafe"
)

func gracefulNative(cmd *exec.Cmd) {
	user := syscall.NewLazyDLL("user32.dll")
	enum := user.NewProc("EnumWindows")
	pidOf := user.NewProc("GetWindowThreadProcessId")
	post := user.NewProc("PostMessageW")
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pid := uint32(cmd.Process.Pid)
		cb := syscall.NewCallback(func(hwnd, unused uintptr) uintptr {
			var owner uint32
			pidOf.Call(hwnd, uintptr(unsafe.Pointer(&owner)))
			if owner == pid {
				post.Call(hwnd, 0x0010, 0, 0)
			}
			return 1
		})
		enum.Call(cb, 0)
		return nil
	}
	cmd.WaitDelay = 3 * time.Second
}
