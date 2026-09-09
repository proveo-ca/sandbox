//go:build !windows

// Package ptyproxy runs a child on a PTY proveo owns, so an overlay can be
// drawn over the agent's full-screen TUI and dismissed without corrupting it.
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
	"time"

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

	escIdle time.Duration

	mu        sync.Mutex
	suspended bool
	buffered  []byte
	overlayIn chan []byte
	inFd      int
}

func New(in, out *os.File) *Proxy {
	return &Proxy{In: in, Out: out, filter: newInputFilter(), escIdle: DefaultEscIdle}
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

	// THE OUT PUMP IS WAITED ON; THE IN PUMP CANNOT BE.
	//
	// `pty.Start` returns with the child ALREADY RUNNING, so a child that writes
	// one line and exits can be gone before this goroutine is first scheduled.
	// The deferred `m.Close()` above then tore down the master with the child's
	// last output still sitting in it — measured at a few percent of runs, and it
	// is the run where the agent died on its last line that most needs a record.
	// So Run does not return until the pump has drained the master.
	//
	// The master is passed EXPLICITLY rather than read back through p.master: the
	// field is nil'd by that same defer, and a pump scheduled late used to fetch
	// the nil and return having copied nothing.
	//
	// pumpIn gets no such treatment because it cannot: it blocks reading the
	// operator's stdin, which never reaches EOF. It is left to discover the closed
	// master on the next keystroke.
	var drained sync.WaitGroup
	drained.Add(1)
	go func() {
		defer drained.Done()
		p.pumpOutFrom(m)
	}()
	go p.pumpIn()

	err = cmd.Wait()
	// Drain BEFORE restoring: these are the child's own bytes, painted for the
	// mode the child was running in. Restoring first puts the terminal back in
	// cooked mode, where ONLCR rewrites the line endings on the way out.
	waitDrained(&drained, drainGrace)
	p.Restore()
	return err
}

// drainGrace caps how long Run waits for the child's last output to reach the
// operator once the child itself has exited.
//
// It is NOT a delay on the normal path. When the child held the last PTY slave,
// the master read fails the moment it exits, so the pump returns in microseconds
// and the wait is over before it began. The cap is for the case where a
// GRANDCHILD inherited the slave and holds the master open: there the pump has
// nothing left to drain and no claim on Run's return.
//
// A read deadline on the master is the obvious alternative and does not work:
// `os.File.SetReadDeadline` returns nil on /dev/ptmx and is then silently
// ignored — MEASURED, a read blocked the full 30s until the child exited. It
// would have looked like a fix and been a no-op.
const drainGrace = 2 * time.Second

// waitDrained waits for wg, but not past grace.
//
// The inner goroutine outlives a timeout only until Run's deferred close makes
// the pump's blocked read fail, so this bounds the wait without leaking.
func waitDrained(wg *sync.WaitGroup, grace time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
	}
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

// DefaultEscIdle is how long a partial escape waits for its continuation
// before being forwarded as the keypress it is. 25-50ms is the conventional
// terminal ESC timeout (vim's ttimeoutlen, kitty's esc-timeout); 40ms sits
// mid-band — long enough that a real Alt-chord or CSI split across two reads
// still arrives whole, short enough to feel instant.
const DefaultEscIdle = 40 * time.Millisecond

type inRead struct {
	b   []byte
	err error
}

// readChunks moves the blocking read off the pump so it can also wait on a
// timer.
func readChunks(r io.Reader) <-chan inRead {
	ch := make(chan inRead, 1)
	go func() {
		defer close(ch)
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				ch <- inRead{b: append([]byte(nil), buf[:n]...)}
			}
			if err != nil {
				ch <- inRead{err: err}
				return
			}
		}
	}()
	return ch
}

func (p *Proxy) pumpIn() { p.pumpInFrom(p.In) }

func (p *Proxy) pumpInFrom(r io.Reader) {
	idleAfter := p.escIdle
	if idleAfter <= 0 {
		idleAfter = DefaultEscIdle
	}
	chunks := readChunks(r)
	var held []byte // a partial escape sequence carried from the previous read
	for {
		var idle <-chan time.Time
		if len(held) > 0 {
			idle = time.After(idleAfter)
		}
		select {
		case c, ok := <-chunks:
			if !ok {
				return
			}
			if len(c.b) > 0 {
				// SPEC: _spec/internal/ptyproxy/terminal-report-filter.puml
				chunk := c.b
				if len(held) > 0 {
					chunk = append(held, chunk...)
					held = nil
				}
				out := chunk
				if !p.DisableFilter && p.filter != nil {
					out, held = p.filter.split(chunk)
				}
				forward := len(out) > 0
				if p.Tap != nil {
					p.Tap(chunk, forward)
				}
				if forward && !p.deliver(out) {
					return
				}
			}
			if c.err != nil {
				return
			}
		case <-idle:
			// No continuation came: it was a keypress, not a prefix. Forward it
			// verbatim — a lone ESC is how the agent's TUI closes a modal.
			out := held
			held = nil
			if p.Tap != nil {
				p.Tap(out, true)
			}
			if !p.deliver(out) {
				return
			}
		}
	}
}

// deliver hands input to the overlay if one is up, else to the child's PTY.
func (p *Proxy) deliver(out []byte) bool {
	p.mu.Lock()
	ch := p.overlayIn
	p.mu.Unlock()
	if ch != nil {
		b := make([]byte, len(out))
		copy(b, out)
		select {
		case ch <- b:
		default: // overlay already answered; drop rather than block the pump
		}
		return true
	}
	_, err := p.masterFile().Write(out)
	return err == nil
}

func (p *Proxy) pumpOutFrom(m io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := m.Read(buf)
		if n > 0 {
			p.onChildOutput(buf[:n])
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

// onChildOutput feeds the transcript tap and the two observed-state watches:
// the child announces its mouse modes, and asks for its cursor position, on
// the same stream it paints on.
// SPEC: _spec/internal/ptyproxy/terminal-report-filter.puml
func (p *Proxy) onChildOutput(b []byte) {
	if p.OutTap != nil {
		p.OutTap(b)
	}
	if !p.DisableFilter && p.filter != nil {
		p.filter.mouse.observe(b)
		p.filter.cpr.observe(b)
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
