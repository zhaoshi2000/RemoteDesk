//go:build !windows

package media

import (
	"os/exec"
	"syscall"
	"time"
)

func gracefulNative(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 3 * time.Second
}
