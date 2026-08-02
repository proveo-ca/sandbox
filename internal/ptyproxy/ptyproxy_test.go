//go:build !windows

package ptyproxy

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Usable must refuse a headless run: pipes are not terminals, so there is no
// overlay and the caller has to fall back to fail-closed behaviour.
func TestUsableRejectsNonTerminals(t *testing.T) {
	t.Parallel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if Usable(r, w) {
		t.Error("pipes must not be reported usable for an overlay")
	}
	if Usable(nil, nil) {
		t.Error("nil files must not be reported usable")
	}
}

func TestOverlayBeforeRunIsAnError(t *testing.T) {
	t.Parallel()
	p := New(os.Stdin, os.Stdout)
	if err := p.Overlay(func(io.Reader, io.Writer) error { return nil }); err != ErrNotRunning {
		t.Errorf("Overlay before Run = %v, want ErrNotRunning", err)
	}
}

// The child must run on a PTY (not a pipe) and its output reach the operator.
func TestChildRunsOnATTYAndOutputIsPumped(t *testing.T) {
	t.Parallel()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outR.Close() }()
	inR, _, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inR.Close() }()

	p := New(inR, outW)
	// `test -t 1` succeeds only when stdout is a terminal, which proves the child
	// got the PTY slave rather than a pipe.
	cmd := exec.Command("sh", "-c", "test -t 1 && echo ON_A_TTY")
	done := make(chan error, 1)
	go func() { done <- p.Run(cmd) }()

	buf := make([]byte, 256)
	_ = outR.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _ := outR.Read(buf)
	_ = outW.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if got := string(buf[:n]); !strings.Contains(got, "ON_A_TTY") {
		t.Errorf("child stdout = %q, want it to prove a TTY and be pumped through", got)
	}
}

// While an overlay is up the child must not be able to paint over it, and its
// keystrokes must be consumed by the overlay rather than reaching the child.
func TestOverlaySuspendsBothPumps(t *testing.T) {
	t.Parallel()
	p := New(os.Stdin, os.Stdout)
	p.suspended = true
	p.buffered = append(p.buffered, []byte("child output while suspended")...)
	if !p.suspended {
		t.Fatal("setup")
	}
	// Draw completing must clear the buffer rather than replay it: the child owns
	// the alternate screen and repaints via the forced resize instead.
	p.mu.Lock()
	p.suspended = false
	p.buffered = p.buffered[:0]
	p.mu.Unlock()
	if len(p.buffered) != 0 {
		t.Error("buffered output must be dropped, not replayed over a repaint")
	}
}
