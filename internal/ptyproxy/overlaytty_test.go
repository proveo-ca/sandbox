//go:build !windows

// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package ptyproxy

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestOverlayScreenFiniReturnsWithoutInput(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devnull.Close() }()

	in := parkedReader{chanReader: &chanReader{ch: make(chan []byte)}, parked: make(chan struct{}, 1)}
	tty := &OverlayTty{in: in, out: devnull, fd: int(devnull.Fd())}
	screen, err := tcell.NewTerminfoScreenFromTty(tty)
	if err != nil {
		t.Fatal(err)
	}
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, in.parked, "tcell's input loop never called Read")
	done := make(chan struct{})
	go func() {
		screen.Fini()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Fini blocked with no input pending: tcell's input loop is parked in OverlayTty.Read and Drain did not wake it")
	}
}

func TestChanReaderHandsBackWhatItNeverRead(t *testing.T) {
	t.Parallel()
	ch := make(chan []byte, 4)
	r := &chanReader{ch: ch}
	ch <- []byte("ab")
	buf := make([]byte, 1)
	if n, err := r.Read(buf); n != 1 || err != nil || buf[0] != 'a' {
		t.Fatalf("Read = (%d,%q,%v), want the first byte", n, buf[:n], err)
	}
	ch <- []byte("cd")
	r.halt()
	if n, err := r.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("Read after halt = (%d,%v), want (0, io.EOF)", n, err)
	}
	if got := string(r.unread()); got != "bcd" {
		t.Fatalf("unread = %q, want %q in arrival order", got, "bcd")
	}
	r.resume()
	ch <- []byte("e")
	if n, err := r.Read(buf); n != 1 || err != nil || buf[0] != 'e' {
		t.Fatalf("Read after resume = (%d,%q,%v), want the next byte", n, buf[:n], err)
	}
}

func TestKeyAfterTheCloseConfirmReachesTheSession(t *testing.T) {
	confirmRounds(t, 1)
}

func TestCloseConfirmReopens(t *testing.T) {
	confirmRounds(t, 3)
}

// confirmRounds drives Ctrl+C → tcell confirm → n → next key, rounds times.
func confirmRounds(t *testing.T, rounds int) {
	t.Setenv("TERM", "xterm-256color")
	opened := make(chan struct{}, 1)
	closed := make(chan struct{}, 1)
	h := startEchoChild(t, func(p *Proxy) {
		p.HandleCtrlC(func(in io.Reader, _ io.Writer) CtrlCAction {
			screen, err := p.OverlayScreen(in)
			if err != nil {
				t.Errorf("OverlayScreen: %v", err)
				return CtrlCStay
			}
			if err := screen.Init(); err != nil {
				t.Errorf("Init: %v", err)
				return CtrlCStay
			}
			opened <- struct{}{}
			for {
				ev := screen.PollEvent()
				if k, ok := ev.(*tcell.EventKey); ok && k.Rune() == 'n' {
					break
				}
				if ev == nil {
					break
				}
			}
			screen.Fini()
			closed <- struct{}{}
			return CtrlCStay
		}, nil)
	})

	for i := 0; i < rounds; i++ {
		key := byte('A' + i)
		h.typeBytes(t, 0x03)
		waitSignal(t, opened, "round %d: Ctrl+C did not open the confirm", i)
		h.typeBytes(t, 'n')
		waitSignal(t, closed, "round %d: Fini hung after n; the confirm stays drawn until another key arrives", i)
		h.typeBytes(t, key)
		h.waitFor(t, seeLine(key), "round %d: the key after n never reached the session", i)
	}
	if got := h.text(); strings.Contains(got, seeLine(0x03)) || strings.Contains(got, seeLine('n')) {
		t.Errorf("the child saw a byte the confirm consumed: %q", got)
	}
}

func TestOverlayHandsUnreadKeysBackToTheSession(t *testing.T) {
	t.Parallel()
	h := startEchoChild(t, nil)

	queued := make(chan struct{})
	go func() {
		_ = h.p.Overlay(func(io.Reader, io.Writer) error {
			h.typeBytes(t, 'B')
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				h.p.mu.Lock()
				n := len(h.p.overlayIn)
				h.p.mu.Unlock()
				if n > 0 {
					close(queued)
					return nil
				}
				time.Sleep(5 * time.Millisecond)
			}
			t.Error("the pump never queued the key for the overlay")
			return nil
		})
	}()
	waitSignal(t, queued, "the overlay never saw a queued key")
	h.waitFor(t, seeLine('B'), "a key the overlay never read was dropped instead of reaching the session")
}

// parkedReader signals each Read before it blocks.
type parkedReader struct {
	*chanReader
	parked chan struct{}
}

func (r parkedReader) Read(p []byte) (int, error) {
	select {
	case r.parked <- struct{}{}:
	default:
	}
	return r.chanReader.Read(p)
}

const echoReady = `import sys, tty
tty.setraw(sys.stdin.fileno())
sys.stdout.buffer.write(b"READY\n")
sys.stdout.buffer.flush()
while True:
    b = sys.stdin.buffer.read(1)
    if not b:
        break
    sys.stdout.buffer.write(b"SEE:%02x\n" % b[0])
    sys.stdout.buffer.flush()
`

func seeLine(b byte) string {
	const hex = "0123456789abcdef"
	return "SEE:" + string([]byte{hex[b>>4], hex[b&0xf]})
}

type echoHarness struct {
	p   *Proxy
	inW *os.File

	mu  sync.Mutex
	out bytes.Buffer
}

// startEchoChild runs echoReady under a Proxy; arm configures it before Run.
func startEchoChild(t *testing.T, arm func(*Proxy)) *echoHarness {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	h := &echoHarness{p: New(inR, outW), inW: inW}
	if arm != nil {
		arm(h.p)
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := outR.Read(buf)
			if n > 0 {
				h.mu.Lock()
				h.out.Write(buf[:n])
				h.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	cmd := exec.Command("python3", "-c", echoReady)
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		_ = h.p.Run(cmd)
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-ran:
		case <-time.After(3 * time.Second):
		}
		_ = inW.Close()
		_ = outW.Close()
		_ = inR.Close()
		_ = outR.Close()
	})
	h.waitFor(t, "READY", "the child never started")
	return h
}

func (h *echoHarness) typeBytes(t *testing.T, b ...byte) {
	if _, err := h.inW.Write(b); err != nil {
		t.Error(err)
	}
}

func (h *echoHarness) text() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.out.String()
}

func (h *echoHarness) waitFor(t *testing.T, want, format string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(h.text(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf(format+"\noutput: %q", append(args, h.text())...)
}

func waitSignal(t *testing.T, ch <-chan struct{}, format string, args ...any) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf(format, args...)
	}
}
