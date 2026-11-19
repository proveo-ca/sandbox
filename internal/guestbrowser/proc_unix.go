//go:build !windows

// SPEC: _spec/_plans/isolated-browser-profiles.puml
package guestbrowser

import (
	"os/exec"
	"syscall"
)

func groupChromium(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killChromium(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
