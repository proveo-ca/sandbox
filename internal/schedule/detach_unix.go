//go:build !windows

package schedule

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// detach starts the watcher in its own session so the launchd tick exiting does not end it.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// terminateChildren sends SIGTERM to every direct child of pid (the command under the pane's sh).
func terminateChildren(pid int) error {
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	if err != nil {
		return err
	}
	var sent int
	for _, f := range strings.Fields(string(out)) {
		if child, err := strconv.Atoi(f); err == nil && syscall.Kill(child, syscall.SIGTERM) == nil {
			sent++
		}
	}
	if sent == 0 {
		return errors.New("no child to signal")
	}
	return nil
}
