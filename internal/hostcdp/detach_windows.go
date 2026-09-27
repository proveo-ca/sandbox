//go:build windows

package hostcdp

import "os/exec"

func detach(*exec.Cmd) {}
