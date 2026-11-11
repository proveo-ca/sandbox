//go:build !windows

// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package ptyproxy

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

var errInCanceled = errors.New("ptyproxy: stdin read cancelled")

// cancelIn is a reader whose blocked Read ends when cancel is called.
type cancelIn struct {
	r      io.Reader
	cancel func()
	close  func()
}

type selectIn struct {
	f        *os.File
	fd, wake int
}

func (s *selectIn) Read(b []byte) (int, error) {
	nfd := max(s.fd, s.wake)
	for {
		var set unix.FdSet
		set.Zero()
		set.Set(s.fd)
		set.Set(s.wake)
		_, err := unix.Select(nfd+1, &set, nil, nil, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if set.IsSet(s.wake) {
			return 0, errInCanceled
		}
		return s.f.Read(b)
	}
}

// newCancelIn wraps f, whose descriptor is fd; it returns f uncancelable when select cannot watch it.
func newCancelIn(f *os.File, fd int) cancelIn {
	plain := cancelIn{r: f, cancel: func() {}, close: func() {}}
	wakeR, wakeW, err := os.Pipe()
	if err != nil {
		return plain
	}
	wake := int(wakeR.Fd())
	if fd < 0 || fd >= unix.FD_SETSIZE || wake >= unix.FD_SETSIZE {
		_ = wakeR.Close()
		_ = wakeW.Close()
		return plain
	}
	return cancelIn{
		r:      &selectIn{f: f, fd: fd, wake: wake},
		cancel: func() { _, _ = wakeW.Write([]byte{0}) },
		close:  func() { _ = wakeR.Close(); _ = wakeW.Close() },
	}
}
