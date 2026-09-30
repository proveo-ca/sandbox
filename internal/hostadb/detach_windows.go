//go:build windows

package hostadb

import "os/exec"

func detach(*exec.Cmd) {}
