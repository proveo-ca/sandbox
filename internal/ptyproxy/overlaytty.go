// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
//go:build !windows

package ptyproxy

import (
	"io"
	"os"

	"github.com/gdamore/tcell/v2"
	"golang.org/x/term"
)

// OverlayTty presents the suspended terminal to a tcell screen.
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

// haltable is a reader Drain can wake and Start can re-arm.
type haltable interface {
	halt()
	resume()
}

// Start puts the terminal in raw mode for tcell and re-arms the reader.
func (t *OverlayTty) Start() error {
	if h, ok := t.in.(haltable); ok {
		h.resume()
	}
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

// Drain wakes a pending Read with io.EOF so tcell's input loop can exit.
func (t *OverlayTty) Drain() error {
	if h, ok := t.in.(haltable); ok {
		h.halt()
	}
	return nil
}

// NotifyResize is unused for the lifetime of a modal — the proxy already
// forwards SIGWINCH to the child, and a resize mid-prompt redraws on the next
// poll.
func (t *OverlayTty) NotifyResize(cb func()) { t.resize = cb }

func (t *OverlayTty) WindowSize() (int, int, error) {
	w, h, err := term.GetSize(t.fd)
	if err != nil {
		return 80, 24, nil // a modal is better mis-sized than not shown
	}
	return w, h, nil
}

// OverlayScreen builds a tcell screen over the suspended terminal.
func (p *Proxy) OverlayScreen(in io.Reader) (tcell.Screen, error) {
	return tcell.NewTerminfoScreenFromTty(&OverlayTty{in: in, out: p.Out, fd: p.fd()})
}
