// Package ptyproxy runs a child on a PTY proveo owns, so an overlay can be drawn
// over the agent's full-screen TUI and dismissed without corrupting it.
//
// SPEC: _spec/internal/reviewgate/pty-review-proxy.puml
package ptyproxy

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// Proxy owns the PTY between the operator's terminal and a child process. Being
// the ONLY reader of the real terminal is the whole point: the child gets the
// slave, so an overlay never contends with it for stdin.
type Proxy struct {
	In  *os.File // the operator's terminal (stdin)
	Out *os.File // the operator's terminal (stdout)

	master  *os.File
	restore *term.State

	mu        sync.Mutex
	suspended bool
	buffered  []byte
}

// New returns a Proxy over the given terminal files. Passing os.Stdin/os.Stdout
// is the normal case.
func New(in, out *os.File) *Proxy { return &Proxy{In: in, Out: out} }

// Usable reports whether a PTY overlay is possible at all: both ends must be a
// real terminal. A headless run has no overlay and must not get one.
func Usable(in, out *os.File) bool {
	return in != nil && out != nil &&
		term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

// Run starts cmd on a PTY, pumps both directions, and waits. The operator's
// terminal is put in raw mode for the duration and restored on every exit path,
// including a signal — raw mode must not survive the process.
func (p *Proxy) Run(cmd *exec.Cmd) error {
	m, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("pty: start: %w", err)
	}
	p.master = m
	defer func() { _ = m.Close() }()

	if st, err := term.MakeRaw(int(p.In.Fd())); err == nil {
		p.restore = st
		defer p.Restore()
	}

	// Real terminal resizes must still reach the child.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			_ = pty.InheritSize(p.In, m)
		}
	}()
	_ = pty.InheritSize(p.In, m)

	go p.pumpIn()
	go p.pumpOut()

	err = cmd.Wait()
	p.Restore()
	return err
}

// Restore returns the operator's terminal to its original mode. Safe to call more
// than once.
func (p *Proxy) Restore() {
	if p.restore != nil {
		_ = term.Restore(int(p.In.Fd()), p.restore)
		p.restore = nil
	}
}

// pumpIn forwards keystrokes to the child, unless an overlay has suspended it —
// then the bytes are consumed here and the child never sees them.
func (p *Proxy) pumpIn() {
	buf := make([]byte, 4096)
	for {
		n, err := p.In.Read(buf)
		if n > 0 {
			p.mu.Lock()
			suspended := p.suspended
			p.mu.Unlock()
			if !suspended {
				if _, werr := p.master.Write(buf[:n]); werr != nil {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// pumpOut writes child output to the terminal, buffering it while an overlay is up
// so nothing paints over the prompt.
func (p *Proxy) pumpOut() {
	buf := make([]byte, 32*1024)
	for {
		n, err := p.master.Read(buf)
		if n > 0 {
			p.mu.Lock()
			if p.suspended {
				p.buffered = append(p.buffered, buf[:n]...)
				p.mu.Unlock()
			} else {
				p.mu.Unlock()
				if _, werr := p.Out.Write(buf[:n]); werr != nil {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// ErrNotRunning is returned when an overlay is requested before Run.
var ErrNotRunning = errors.New("ptyproxy: no child running")

// Overlay suspends both pumps, hands the terminal to draw, then restores the
// child's display. draw owns the terminal exclusively for its duration.
//
// Restoring does NOT replay the buffered output: the child owns the alternate
// screen and would repaint over it anyway. Instead the child is made to repaint by
// a genuine resize — one column narrower, then back. Both deliver SIGWINCH, and
// because the dimensions actually change, a TUI that early-outs on a no-op resize
// still does a full relayout. A bare SIGWINCH at unchanged size is unreliable.
func (p *Proxy) Overlay(draw func(in io.Reader, out io.Writer) error) error {
	if p.master == nil {
		return ErrNotRunning
	}
	p.mu.Lock()
	p.suspended = true
	p.buffered = p.buffered[:0]
	p.mu.Unlock()

	// Leave raw mode so draw can use ordinary line editing if it wants to.
	if p.restore != nil {
		_ = term.Restore(int(p.In.Fd()), p.restore)
	}
	drawErr := draw(p.In, p.Out)
	if st, err := term.MakeRaw(int(p.In.Fd())); err == nil {
		p.restore = st
	}

	p.mu.Lock()
	p.suspended = false
	p.buffered = p.buffered[:0]
	p.mu.Unlock()

	p.forceRepaint()
	return drawErr
}

// forceRepaint nudges the child into a full redraw via a real size change.
func (p *Proxy) forceRepaint() {
	size, err := pty.GetsizeFull(p.master)
	if err != nil || size.Cols == 0 {
		return
	}
	shrunk := *size
	shrunk.Cols = size.Cols - 1
	_ = pty.Setsize(p.master, &shrunk)
	_ = pty.Setsize(p.master, size)
}
