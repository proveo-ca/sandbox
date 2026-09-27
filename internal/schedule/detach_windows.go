//go:build windows

package schedule

import "os/exec"

func detach(*exec.Cmd) {}
