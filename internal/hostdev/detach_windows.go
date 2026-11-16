//go:build windows

package hostdev

import "os/exec"

func detach(*exec.Cmd) {}
