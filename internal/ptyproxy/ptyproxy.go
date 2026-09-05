//go:build !windows

// Package ptyproxy runs a child on a PTY proveo owns, so an overlay can be
// drawn over the agent's full-screen TUI and dismissed without corrupting it.
//
// SPEC: _spec/internal/reviewgate/pty-review-proxy.puml, _spec/internal/runlog/run-transcript.puml
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

type Proxy struct {
	In  *os.File // the operator's terminal (stdin)
	Out *os.File // the operator's terminal (stdout)

	master  *os.File
	restore *term.State

	Tap func(b []byte, forwarded bool)

	OutTap func(b []byte)

	filter        *inputFilter
	DisableFilter bool

	DropReports bool

	mu        sync.Mutex
	suspended bool
	buffered  []byte
	overlayIn chan []byte
	inFd      int
}

func New(in, out *os.File) *Proxy {
	return &Proxy{In: in, Out: out, filter: newInputFilter()}
}

func Usable(in, out *os.File) bool {
	return in != nil && out != nil &&
		term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

func (p *Proxy) Run(cmd *exec.Cmd) error {
	if p.filter != nil {
		p.filter.dropReplies = p.DropReports
	}
	m, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("pty: start: %w", err)
	}
	p.mu.Lock()
	p.master = m
	p.inFd = int(p.In.Fd())
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.master = nil
		p.mu.Unlock()
		_ = m.Close()
	}()

	if st, err := term.MakeRaw(p.fd()); err == nil {
		p.setRestore(st)
		defer p.Restore()
	}

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

// Restore returns the operator's terminal to its original mode.
func (p *Proxy) Restore() {
	p.mu.Lock()
	st, fd := p.restore, p.inFd
	p.restore = nil
	p.mu.Unlock()
	if st != nil {
		_ = term.Restore(fd, st)
	}
}

func (p *Proxy) fd() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inFd
}

func (p *Proxy) setRestore(st *term.State) {
	p.mu.Lock()
	p.restore = st
	p.mu.Unlock()
}

func (p *Proxy) pumpIn() {
	buf := make([]byte, 4096)
	for {
		n, err := p.In.Read(buf)
		if n > 0 {
			forward := p.DisableFilter || p.filter == nil || p.filter.keep(buf[:n])
			if p.Tap != nil {
				p.Tap(buf[:n], forward)
			}
			if !forward {
				continue
			}
			p.mu.Lock()
			ch := p.overlayIn
			p.mu.Unlock()
			if ch != nil {
				b := make([]byte, n)
				copy(b, buf[:n])
				select {
				case ch <- b:
				default: // overlay already answered; drop rather than block the pump
				}
			} else if _, werr := p.masterFile().Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *Proxy) pumpOut() {
	buf := make([]byte, 32*1024)
	m := p.masterFile()
	for {
		n, err := m.Read(buf)
		if n > 0 {
			if p.OutTap != nil {
				p.OutTap(buf[:n])
			}
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
func (p *Proxy) Overlay(draw func(in io.Reader, out io.Writer) error) error {
	p.mu.Lock()
	running := p.master != nil
	p.mu.Unlock()
	if !running {
		return ErrNotRunning
	}
	in := make(chan []byte, 16)
	p.mu.Lock()
	p.suspended = true
	p.buffered = p.buffered[:0]
	p.overlayIn = in
	p.mu.Unlock()

	drawErr := draw(&chanReader{ch: in}, p.Out)

	p.mu.Lock()
	p.suspended = false
	p.buffered = p.buffered[:0]
	p.overlayIn = nil
	p.mu.Unlock()

	p.forceRepaint()
	return drawErr
}

func (p *Proxy) forceRepaint() {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := p.master
	if m == nil {
		return
	}
	size, err := pty.GetsizeFull(m)
	if err != nil || size.Cols == 0 {
		return
	}
	shrunk := *size
	shrunk.Cols = size.Cols - 1
	_ = pty.Setsize(m, &shrunk)
	_ = pty.Setsize(m, size)
}

func (p *Proxy) masterFile() *os.File {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.master
}

type chanReader struct {
	ch  chan []byte
	buf []byte
}

func (r *chanReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		b, ok := <-r.ch
		if !ok {
			return 0, io.EOF
		}
		r.buf = b
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
