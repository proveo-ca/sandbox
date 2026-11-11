//go:build darwin || freebsd || netbsd || openbsd || dragonfly

// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package ptyproxy

import (
	"os"

	"golang.org/x/sys/unix"
)

// flushRead is FREAD, the input-queue bit TIOCFLUSH takes.
const flushRead = 0x1

// DiscardInput drops what was typed at terminal f and not yet read.
func DiscardInput(f *os.File) {
	if f != nil {
		_ = unix.IoctlSetPointerInt(int(f.Fd()), unix.TIOCFLUSH, flushRead)
	}
}
