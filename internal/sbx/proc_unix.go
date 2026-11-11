//go:build !windows

// SPEC: _spec/internal/backend/teardown.puml
package sbx

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ownGroup starts cmd in its own process group; cancelling cmd then kills the whole group.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
