//go:build !windows

package schedule

import (
	"os/exec"
	"syscall"
)

// detach starts the watcher in its own session so the launchd tick exiting does not end it.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
