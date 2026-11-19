//go:build windows

// SPEC: _spec/internal/backend/teardown.puml
package sandbox

import (
	"errors"
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

// ownGroup is a no-op: Windows delivers Ctrl+C by console, not by process group.
func ownGroup(*exec.Cmd) {}

func tryProfileLock(f *os.File) (busy bool, err error) {
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &windows.Overlapped{})
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION), err
}
