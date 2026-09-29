//go:build windows

package schedule

import (
	"errors"
	"os/exec"
)

func detach(*exec.Cmd) {}

func terminateChildren(int) error { return errors.ErrUnsupported }
