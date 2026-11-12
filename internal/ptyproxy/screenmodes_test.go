// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
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
)

func TestScreenModesFollowTheChildsAlternateScreenAndCursor(t *testing.T) {
	for _, c := range []struct {
		name   string
		chunks []string
		want   string
	}{
		{"a child that set nothing", []string{"hello\r\n"}, ""},
		{"the alternate screen", []string{"\x1b[?1049h"}, "\x1b[?1049h"},
		{"the older alternate-screen modes", []string{"\x1b[?47h"}, "\x1b[?1049h"},
		{"a hidden cursor", []string{"\x1b[?25l"}, "\x1b[?25l"},
		{"both, in one sequence", []string{"\x1b[?1049;25h\x1b[?25l"}, "\x1b[?1049h\x1b[?25l"},
		{"a sequence split across two reads", []string{"paint\x1b[?10", "49h more"}, "\x1b[?1049h"},
		{"left again", []string{"\x1b[?1049h\x1b[?25l", "\x1b[?1049l\x1b[?25h"}, ""},
		{"a mode that is neither", []string{"\x1b[?2004h\x1b[?1000h\x1b[4h"}, ""},
	} {
		var s screenModes
		for _, chunk := range c.chunks {
			s.observe([]byte(chunk))
		}
		if got := string(s.reassert()); got != c.want {
			t.Errorf("%s: reassert = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestScreenModesRememberTheAlternateScreenAfterTheChildLeavesIt(t *testing.T) {
	for _, c := range []struct {
		name   string
		chunks []string
		want   bool
	}{
		{"an inline child", []string{"hello\r\nworld\r\n"}, false},
		{"entered, then left before the exit", []string{"\x1b[?1049h drawn", "\x1b[?1049l"}, true},
		{"split across two reads", []string{"boot\r\n\x1b[?10", "49hdrawn\r\n"}, true},
		{"inside a mode list", []string{"\x1b[?25;1047h"}, true},
		{"only left, never entered", []string{"\x1b[?1049l"}, false},
	} {
		var s screenModes
		for _, chunk := range c.chunks {
			s.observe([]byte(chunk))
		}
		if got := s.enteredAlt(); got != c.want {
			t.Errorf("%s: enteredAlt = %v, want %v", c.name, got, c.want)
		}
	}
	var none *Proxy
	if none.EnteredAltScreen() {
		t.Error("a run with no proxy read as one on the alternate screen")
	}
}

// outLog collects what the proxy writes to the operator's terminal.
type outLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (o *outLog) drain(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		o.mu.Lock()
		o.b.Write(buf[:n])
		o.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (o *outLog) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

func (o *outLog) await(t *testing.T, want string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if strings.Contains(o.String(), want) {
			return
		}
	}
	t.Fatalf("the terminal never received %q; got %q", want, o.String())
}

func TestAnOverlayGivesTheChildItsScreenModesBack(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	term := &outLog{}
	go term.drain(outR)

	p := New(inR, outW)
	cmd := exec.Command("sh", "-c", `printf '\033[?1049h\033[?25lCHILD_PAINTED'; sleep 30`)
	ran := make(chan struct{})
	go func() { defer close(ran); _ = p.Run(cmd) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-ran
	})
	term.await(t, "CHILD_PAINTED")

	const left = "\x1b[?1049l\x1b[?25hOVERLAY_DONE"
	if err := p.Overlay(func(_ io.Reader, out io.Writer) error {
		_, err := out.Write([]byte(left))
		return err
	}); err != nil {
		t.Fatalf("Overlay: %v", err)
	}
	term.await(t, left+"\x1b[?1049h\x1b[?25l")
}
