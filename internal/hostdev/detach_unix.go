//go:build !windows

package hostdev

import (
	"os/exec"
	"syscall"
)

// detach moves a launched browser or emulator out of proveo's process group so Ctrl-C and terminal hangup spare it.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
