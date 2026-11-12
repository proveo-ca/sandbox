// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package ptyproxy

import (
	"os"

	"golang.org/x/sys/unix"
)

// DiscardInput drops what was typed at terminal f and not yet read.
func DiscardInput(f *os.File) {
	if f != nil {
		_ = unix.IoctlSetInt(int(f.Fd()), unix.TCFLSH, unix.TCIFLUSH)
	}
}
