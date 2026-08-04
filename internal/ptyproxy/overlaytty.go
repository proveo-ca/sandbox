//go:build !windows

package ptyproxy

import (
	"io"
	"os"

	"github.com/gdamore/tcell/v2"
	"golang.org/x/term"
)

// OverlayTty presents the suspended terminal to a tcell screen.
//
// tcell's default screen opens /dev/tty and runs its own input loop, which would
// make it a SECOND reader competing with the pump — the exact contention this
// package exists to remove, and the reason a rendered modal took no keystrokes.
// Feeding tcell through this adapter keeps the pump the sole reader: input comes
// from the hand-off channel, output goes to the operator's terminal.
type OverlayTty struct {
	in  io.Reader
	out *os.File
	fd  int

	saved  *term.State
	resize func()
}

var _ tcell.Tty = (*OverlayTty)(nil)

func (t *OverlayTty) Read(p []byte) (int, error)  { return t.in.Read(p) }
func (t *OverlayTty) Write(p []byte) (int, error) { return t.out.Write(p) }
func (t *OverlayTty) Close() error                { return nil } // the proxy owns these files

// Start puts the terminal in raw mode for tcell. The pump is already parked, so
// this cannot race a read in flight.
func (t *OverlayTty) Start() error {
	if st, err := term.MakeRaw(t.fd); err == nil {
		t.saved = st
	}
	return nil
}

// Stop restores whatever mode was in effect before the overlay.
func (t *OverlayTty) Stop() error {
	if t.saved != nil {
		_ = term.Restore(t.fd, t.saved)
		t.saved = nil
	}
	return nil
}

// Drain is a no-op: the reader wakes when the channel closes, so tcell never
// blocks forever the way it can on a real /dev/tty.
func (t *OverlayTty) Drain() error { return nil }

// NotifyResize is unused for the lifetime of a modal — the proxy already forwards
// SIGWINCH to the child, and a resize mid-prompt redraws on the next poll.
func (t *OverlayTty) NotifyResize(cb func()) { t.resize = cb }

func (t *OverlayTty) WindowSize() (int, int, error) {
	w, h, err := term.GetSize(t.fd)
	if err != nil {
		return 80, 24, nil // a modal is better mis-sized than not shown
	}
	return w, h, nil
}

// OverlayScreen builds a tcell screen over the suspended terminal. Only valid
// inside Overlay's draw callback, where the pump is parked and feeding in.
func (p *Proxy) OverlayScreen(in io.Reader) (tcell.Screen, error) {
	return tcell.NewTerminfoScreenFromTty(&OverlayTty{in: in, out: p.Out, fd: p.fd()})
}
