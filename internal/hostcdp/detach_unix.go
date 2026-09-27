//go:build !windows

package hostcdp

import (
	"os/exec"
	"syscall"
)

// detach moves the browser out of proveo's process group so Ctrl-C and terminal hangup spare it.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
