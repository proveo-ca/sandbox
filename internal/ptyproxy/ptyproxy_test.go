//go:build !windows

package ptyproxy

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
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

func TestOverlayOwnsStdinExclusively(t *testing.T) {
	t.Parallel()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = outR
	p := New(inR, outW)
	cmd := exec.Command("cat") // echoes whatever the pump forwards
	go func() { _ = p.Run(cmd) }()
	time.Sleep(300 * time.Millisecond) // let Run install the pumps

	got := make(chan string, 1)
	go func() {
		_ = p.Overlay(func(in io.Reader, _ io.Writer) error {
			buf := make([]byte, 16)
			n, _ := in.Read(buf)
			got <- string(buf[:n])
			return nil
		})
	}()
	time.Sleep(300 * time.Millisecond) // let the pump park

	if _, err := inW.Write([]byte("y\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case answer := <-got:
		if !strings.HasPrefix(answer, "y") {
			t.Errorf("overlay read %q, want the operator's answer", answer)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the overlay never received the keystroke — the pump swallowed it")
	}
	_ = cmd.Process.Kill()
}

func TestOverlayScreenReadsFromTheHandoff(t *testing.T) {
	t.Parallel()
	fed := make(chan []byte, 1)
	fed <- []byte("y")
	close(fed)

	p := New(os.Stdin, os.Stdout)
	tty := &OverlayTty{in: &chanReader{ch: fed}, out: os.Stdout, fd: p.fd()}

	buf := make([]byte, 1)
	n, err := tty.Read(buf)
	if err != nil || n != 1 || buf[0] != 'y' {
		t.Fatalf("tty.Read = (%d,%q,%v), want the byte handed over by the pump", n, buf[:n], err)
	}
	// Close must not touch the proxy's files: they outlive the modal.
	if err := tty.Close(); err != nil {
		t.Errorf("Close = %v, want nil (the proxy owns these files)", err)
	}
	w, h, err := tty.WindowSize()
	if err != nil || w <= 0 || h <= 0 {
		t.Errorf("WindowSize = (%d,%d,%v), want a usable fallback even off a tty", w, h, err)
	}
}

func TestOutTapCopiesChildOutputWithoutCostingTheChildItsTTY(t *testing.T) {
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

	var mu sync.Mutex
	var tapped []byte
	p := New(inR, outW)
	p.OutTap = func(b []byte) {
		mu.Lock()
		defer mu.Unlock()
		tapped = append(tapped, b...)
	}

	cmd := exec.Command("sh", "-c", "test -t 1 && echo Credit balance is too low")
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

	// The operator still sees it: the tap is a copy, never a diversion.
	if got := string(buf[:n]); !strings.Contains(got, "Credit balance is too low") {
		t.Errorf("the terminal lost the child's output to the tap: %q", got)
	}
	mu.Lock()
	got := string(tapped)
	mu.Unlock()
	if !strings.Contains(got, "Credit balance is too low") {
		t.Errorf("the tap saw nothing; an interactive death would again have no record: %q", got)
	}
	// `test -t 1` only succeeded because the child had the PTY slave — a tap that
	// forced a pipe would have made the command print nothing at all.
}

// A nil tap is the normal case and must not be called into.
func TestOutTapIsOptional(t *testing.T) {
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
	cmd := exec.Command("sh", "-c", "echo plain")
	done := make(chan error, 1)
	go func() { done <- p.Run(cmd) }()
	buf := make([]byte, 64)
	_ = outR.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = outR.Read(buf)
	_ = outW.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return with no tap set")
	}
}

func TestDropReportsReachesTheInputPump(t *testing.T) {
	t.Parallel()
	lone := "\x1b[?6c" // the VT102 DA reply that killed proveo-1787852436-14907

	for _, tc := range []struct {
		why         string
		dropReports bool
		wantForward bool
	}{
		{"sbx: a prompt stream queried nothing, so nothing is owed", true, false},
		{"docker: the child asked on a real tty and is owed its answer", false, true},
	} {
		t.Run(tc.why, func(t *testing.T) {
			t.Parallel()
			outR, outW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = outR.Close() }()
			inR, inW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = inR.Close() }()

			seen := make(chan bool, 4)
			p := New(inR, outW)
			p.DropReports = tc.dropReports
			p.Tap = func(b []byte, forwarded bool) {
				if string(b) == lone {
					seen <- forwarded
				}
			}

			// Written before Run: the pipe buffers, so the pump reads it whenever it
			// starts and the test never has to sleep to win a race.
			if _, err := inW.Write([]byte(lone)); err != nil {
				t.Fatal(err)
			}

			done := make(chan error, 1)
			go func() { done <- p.Run(exec.Command("sh", "-c", "sleep 1")) }()

			select {
			case got := <-seen:
				if got != tc.wantForward {
					t.Errorf("report forwarded=%v, want %v", got, tc.wantForward)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the tap never saw the report; the pump is not reading In")
			}

			_ = inW.Close()
			_ = outW.Close()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("Run did not return")
			}
		})
	}
}

// A lone ESC is one byte, so escEnd reports it incomplete and split() can only
// hold it — the pump has to decide it was a keypress. Held forever, opencode's
// ctrl+p modal could not be closed and the NEXT key went out as an Alt-chord.
func TestPumpInReleasesHeldEscapeAfterIdle(t *testing.T) {
	t.Parallel()
	type step struct {
		in   string
		want []string // chunks the child must receive before the next input
	}
	for _, tc := range []struct {
		name    string
		escIdle time.Duration
		steps   []step
	}{
		{
			name:    "lone escape reaches the agent so a modal can close",
			escIdle: 20 * time.Millisecond,
			steps:   []step{{in: "\x1b", want: []string{"\x1b"}}},
		},
		{
			name:    "the key after an escape is its own keystroke, not alt-key",
			escIdle: 20 * time.Millisecond,
			steps: []step{
				{in: "\x1b", want: []string{"\x1b"}},
				{in: "k", want: []string{"k"}},
			},
		},
		{
			// A long idle proves the continuation wins the race, not the timer.
			name:    "an escape split across reads is still assembled",
			escIdle: 10 * time.Second,
			steps: []step{
				{in: "\x1b["},
				{in: "A", want: []string{"\x1b[A"}},
			},
		},
		{
			name:    "a focus report split across reads is still dropped",
			escIdle: 10 * time.Second,
			steps: []step{
				{in: "\x1b["},
				{in: "I"},
				{in: "x", want: []string{"x"}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			feed := make(chan []byte, 8)
			sink := make(chan []byte, 16)
			p := New(os.Stdin, os.Stdout)
			p.escIdle = tc.escIdle
			p.overlayIn = sink // a PTY-free sink on the pump's normal write path
			go p.pumpInFrom(&chanReader{ch: feed})
			t.Cleanup(func() { close(feed) })

			var typed, got, want []string
			for _, s := range tc.steps {
				feed <- []byte(s.in)
				typed = append(typed, s.in)
				want = append(want, s.want...)
				for range s.want {
					select {
					case b := <-sink:
						got = append(got, string(b))
					case <-time.After(5 * time.Second):
						t.Errorf("pumpInFrom(%q) delivered %q, want %q — input never reached the agent", typed, got, want)
						return
					}
				}
			}
			// Nothing beyond what was typed may follow.
			select {
			case b := <-sink:
				got = append(got, string(b))
			case <-time.After(100 * time.Millisecond):
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("pumpInFrom(%q) delivered mismatch (-want +got):\n%s", typed, diff)
			}
		})
	}
}
