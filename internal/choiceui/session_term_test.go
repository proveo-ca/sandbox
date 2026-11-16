// SPEC: _spec/internal/choiceui/session.puml
//go:build !windows

package choiceui

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/creack/pty"

	"github.com/proveo-ca/proveo/internal/ui"
)

// waitLimit is how long a test waits for the Program to draw or answer.
const waitLimit = 10 * time.Second

// openFake is a Session on a pipe: the test types into keys, and the Program draws where nobody reads.
func openFake(t *testing.T) (*Session, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s, err := startSession(func() (io.Reader, io.Writer, func(), error) { return r, io.Discard, func() {}, nil },
		tea.WithWindowSize(100, 30))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(); _ = w.Close(); _ = r.Close() })
	return s, w
}

// waitView is the screen once it shows want.
func waitView(t *testing.T, s *Session, want string) string {
	t.Helper()
	var last string
	for end := time.Now().Add(waitLimit); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		v, err := s.peek()
		if err != nil {
			t.Fatalf("peek: %v", err)
		}
		if strings.Contains(v, want) {
			return v
		}
		last = v
	}
	t.Fatalf("the screen never showed %q:\n%s", want, last)
	return ""
}

// within is the value on c, or fails after waitLimit.
func within[T any](t *testing.T, c <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(waitLimit):
		t.Fatalf("%s: nothing after %s", what, waitLimit)
	}
	var zero T
	return zero
}

func typeKeys(t *testing.T, w io.Writer, keys string) {
	t.Helper()
	if _, err := io.WriteString(w, keys); err != nil {
		t.Fatal(err)
	}
}

type asked struct {
	out Outcome
	err error
}

func TestAWalkOfPagesRunsOnOneSession(t *testing.T) {
	s, keys := openFake(t)
	outs := make(chan asked, 4)
	proceed := make(chan struct{})
	go func() {
		ask := func(f *Form) {
			out, err := s.Ask(f)
			outs <- asked{out, err}
		}
		ask(pageOne(keepAsIs))
		s.Plan([]string{"Docker login"})
		s.Status("Docker login", MarkRunning, "sbx login · Docker not signed in")
		s.Status("Docker login", MarkDone, "signed in")
		<-proceed
		ask(pageThree())
		ask(pageOne(keepAsIs))
		ask(pageThree())
	}()
	expect := func(want Outcome, what string) {
		t.Helper()
		if got := within(t, outs, what); got.err != nil || got.out != want {
			t.Fatalf("%s = %v, %v; want %v", what, got.out, got.err, want)
		}
	}

	waitView(t, s, "Prepare sbx on this")
	typeKeys(t, keys, "\r")
	expect(Accepted, "enter on page 1")
	view := waitView(t, s, "✓ Docker login  signed in")
	if !strings.Contains(view, "running 1 of 1 — Docker login") {
		t.Errorf("the running page lacks its key line:\n%s", view)
	}
	close(proceed)
	waitView(t, s, "Store agent credentials")
	typeKeys(t, keys, "\x1b")
	expect(Back, "esc on page 3")
	waitView(t, s, "Prepare sbx on this")
	typeKeys(t, keys, "\r")
	expect(Accepted, "enter on page 1 again")
	waitView(t, s, "Store agent credentials")
	typeKeys(t, keys, "\x03")
	waitView(t, s, "Quit init?")
	typeKeys(t, keys, "\x03")
	expect(Quit, "ctrl+c twice on page 3")

	if err := s.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
	if s.Context().Err() == nil {
		t.Error("Close left the walk's context live")
	}
	if _, err := s.Ask(pageOne(keepAsIs)); !errors.Is(err, ErrSessionEnded) {
		t.Errorf("Ask after Close = %v, want ErrSessionEnded", err)
	}
}

func TestCtrlCWhileRunningCancelsTheContext(t *testing.T) {
	s, keys := openFake(t)
	outs := make(chan asked, 1)
	go func() {
		out, err := s.Ask(pageOne(keepAsIs))
		outs <- asked{out, err}
	}()
	waitView(t, s, "Prepare sbx on this")
	typeKeys(t, keys, "\r")
	within(t, outs, "enter")
	s.Plan([]string{"Docker login"})
	s.Status("Docker login", MarkRunning, "sbx login")
	typeKeys(t, keys, "\x03")
	within(t, s.Context().Done(), "ctrl+c while running")
	waitView(t, s, "stopping after Docker login …")
}

func TestTheDriverReadsAnswersWhileTheRowsRun(t *testing.T) {
	s, keys := openFake(t)
	f := pageTwo()
	outs := make(chan asked, 1)
	go func() {
		out, err := s.Ask(f)
		outs <- asked{out, err}
	}()
	waitView(t, s, "Set up git and the shell")
	typeKeys(t, keys, "\r")
	if got := within(t, outs, "enter on page 2"); got.out != Accepted {
		t.Fatalf("enter on page 2 = %v, %v; want Accepted", got.out, got.err)
	}
	stop := make(chan struct{})
	reads := make(chan string, 1)
	go func() {
		for {
			read := f.Selection("git identity") + " · " + f.FieldValue("user.name")
			select {
			case <-stop:
				reads <- read
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	s.Plan([]string{"git identity"})
	s.Status("git identity", MarkRunning, "git config --global")
	waitView(t, s, "running 1 of 1 — git identity")
	s.Status("git identity", MarkDone, "Ada Example <ada@example.invalid>")
	waitView(t, s, "git identity  Ada Example")
	close(stop)
	if read := within(t, reads, "the driver's reads"); read != "now · Ada Example" {
		t.Errorf("the driver read %q during the run, want %q", read, "now · Ada Example")
	}
}

func TestACtrlCAsTheChildExitsStillStopsTheWalk(t *testing.T) {
	caught := make(chan os.Signal, 1)
	signal.Notify(caught, os.Interrupt)
	t.Cleanup(func() { signal.Stop(caught) })
	s, keys := openFake(t)
	outs := make(chan asked, 1)
	go func() {
		out, err := s.Ask(pageOne(keepAsIs))
		outs <- asked{out, err}
	}()
	waitView(t, s, "Prepare sbx on this")
	typeKeys(t, keys, "\r")
	if got := within(t, outs, "enter on page 1"); got.out != Accepted {
		t.Fatalf("enter on page 1 = %v, %v; want Accepted", got.out, got.err)
	}
	s.Plan([]string{"Docker login"})
	s.Status("Docker login", MarkRunning, "sbx login")
	err := s.Hand("sbx login", []string{"sbx's screen"}, func() error { return nil }, func(error) (Mark, string) {
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Error(err)
		}
		select {
		case <-caught:
		case <-time.After(waitLimit):
			t.Error("no SIGINT arrived")
		}
		return MarkDone, "signed in"
	})
	if err != nil {
		t.Fatalf("Hand = %v", err)
	}
	within(t, s.Context().Done(), "a SIGINT after the child exited")
	waitView(t, s, "stopping after Docker login …")
}

func TestANestedProgramIsRefusedWhileASessionHoldsTheTerminal(t *testing.T) {
	var term bytes.Buffer
	prev := ui.Default
	ui.Default = ui.New(&term)
	t.Cleanup(func() { ui.Default = prev })
	s, _ := openFake(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := stepForm(1, 1).RunStep(); !errors.Is(err, ErrTerminalHeld) {
			t.Errorf("RunStep under a session = %v, want ErrTerminalHeld", err)
		}
		if _, asked := AskDrawsGlyphs(); asked {
			t.Error("a dialog asked on the terminal a session holds")
		}
		if _, _, err := (&FilterList{Items: []string{"a"}}).Run(); !errors.Is(err, ErrTerminalHeld) {
			t.Errorf("a filter list under a session = %v, want ErrTerminalHeld", err)
		}
		opened := false
		if _, err := startSession(func() (io.Reader, io.Writer, func(), error) {
			opened = true
			return nil, nil, nil, errors.New("unreachable")
		}); !errors.Is(err, ErrTerminalHeld) || opened {
			t.Errorf("a second session = %v (opened %v), want ErrTerminalHeld before any open", err, opened)
		}
		if _, err := OpenSession(); !errors.Is(err, ErrTerminalHeld) {
			t.Errorf("OpenSession under a session = %v, want ErrTerminalHeld", err)
		}
	}()
	within(t, done, "the refusals")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(term.String(), "what does your terminal draw here? — not asked: a card holds the terminal") {
		t.Errorf("the refused dialog left no warning after the card: %q", term.String())
	}
	if err := claimTerminal(); err != nil {
		t.Errorf("Close did not free the terminal: %v", err)
	}
	freeTerminal()
}

func TestHeldLinesReplayAfterTheCard(t *testing.T) {
	var term bytes.Buffer
	prev := ui.Default
	ui.Default = ui.New(&term)
	t.Cleanup(func() { ui.Default = prev })
	s, _ := openFake(t)
	ui.Warnf("a stray line while the card is up")
	if term.Len() != 0 {
		t.Fatalf("a line reached the terminal under the card: %q", term.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(term.String(), "a stray line while the card is up") {
		t.Errorf("Close did not replay the held line: %q", term.String())
	}
	if ui.Default.W != &term {
		t.Error("Close did not give ui.Default back")
	}
}

func TestACtrlCDuringAHandOffStopsTheWalkAfterIt(t *testing.T) {
	caught := make(chan os.Signal, 1)
	signal.Notify(caught, os.Interrupt)
	t.Cleanup(func() { signal.Stop(caught) })
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	t.Cleanup(func() { signal.Stop(sigs) })
	d := newDriven(t, 80, 24)
	f := running(t, d)
	reply := make(chan error, 1)
	d.send(handMsg{
		command: "sh",
		sigs:    sigs,
		run: func() error {
			if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
				return err
			}
			select {
			case <-caught:
			case <-time.After(waitLimit):
				return errors.New("no SIGINT arrived")
			}
			return exec.Command("/bin/sh", "-c", "kill -INT $$").Run()
		},
		outcome: func(error) (Mark, string) { return MarkNeeds, "stopped · Ctrl+C ended it" },
		reply:   reply,
	})
	err := got(t, reply, "hand")
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("Hand = %v, want the child's exit error as run gave it", err)
	}
	if ws, ok := exit.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGINT {
		t.Errorf("the child ended %v, want killed by SIGINT", exit)
	}
	if !d.stopped() || !f.Progress.Stopping {
		t.Errorf("a Ctrl+C during a hand-off: stopped %v, stopping %v; want both", d.stopped(), f.Progress.Stopping)
	}
	if view := d.view(); !strings.Contains(view, "! Docker login  stopped · Ctrl+C ended it") {
		t.Errorf("the row lacks the hand-off's outcome:\n%s", view)
	}
}

// screenLog is what the program side of a pty printed, as raw bytes.
type screenLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *screenLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *screenLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// waitFor is the screen log once want shows count times.
func (l *screenLog) waitFor(t *testing.T, want string, count int) string {
	t.Helper()
	for end := time.Now().Add(waitLimit); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if s := l.String(); strings.Count(s, want) >= count {
			return s
		}
	}
	t.Fatalf("the terminal never printed %q %d times:\n%q", want, count, l.String())
	return ""
}

// Alternate-screen modes as Bubble Tea writes them.
const (
	altEnter = "\x1b[?1049h"
	altLeave = "\x1b[?1049l"
)

func TestAHandOffRunsARealChildAndReturnsToTheCard(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty here: %v", err)
	}
	t.Cleanup(func() { _ = tty.Close(); _ = ptmx.Close() })
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	screen := &screenLog{}
	go func() { _, _ = io.Copy(screen, ptmx) }()
	s, err := startSession(func() (io.Reader, io.Writer, func(), error) { return tty, tty, func() {}, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	outs := make(chan asked, 1)
	go func() {
		out, err := s.Ask(pageOne(keepAsIs))
		outs <- asked{out, err}
	}()
	waitView(t, s, "Prepare sbx on this")
	typeKeys(t, ptmx, "\r")
	if got := within(t, outs, "enter on page 1"); got.out != Accepted {
		t.Fatalf("enter on page 1 = %v, %v", got.out, got.err)
	}
	s.Plan([]string{"Docker login"})
	s.Status("Docker login", MarkRunning, "sbx login · Docker not signed in")

	child := exec.Command("/bin/sh", "-c", `read x; printf 'got:%s\n' "$x"`)
	child.Stdin, child.Stdout, child.Stderr = tty, tty, tty
	handed := make(chan error, 1)
	began := time.Now()
	go func() {
		handed <- s.Hand("sbx login", []string{"Docker not signed in", "sbx's screen", "page 1/4 returns when it exits"},
			child.Run, func(err error) (Mark, string) {
				if err != nil {
					return MarkFailed, err.Error()
				}
				return MarkDone, "signed in"
			})
	}()
	screen.waitFor(t, "sbx's screen", 1)
	t.Logf("the frame showed %s after Hand", time.Since(began).Round(time.Millisecond))
	typeKeys(t, ptmx, "hello\r")
	if err := within(t, handed, "the hand-off"); err != nil {
		t.Fatalf("Hand = %v", err)
	}
	raw := screen.waitFor(t, altEnter, 2)
	view := waitView(t, s, "✓ Docker login  signed in")

	leave := strings.Index(raw, altLeave)
	frame := strings.Index(raw, "sbx's screen")
	gotLine := strings.Index(raw, "got:hello")
	row := gotLine + strings.Index(raw[max(gotLine, 0):], "Docker login  signed in")
	back := gotLine + strings.Index(raw[max(gotLine, 0):], altEnter)
	if leave < 0 || leave >= frame || frame >= gotLine || gotLine >= row || row >= back {
		t.Errorf("want leave < frame < child < outcome row < back; got %d %d %d %d %d:\n%q", leave, frame, gotLine, row, back, raw)
	}
	if !strings.Contains(raw[frame:gotLine], "hello") {
		t.Errorf("the terminal did not echo the typed line: it was not handed back in cooked mode:\n%q", raw[frame:gotLine])
	}
	if !strings.Contains(view, "running 1 of 1 — Docker login") {
		t.Errorf("the card came back without its run:\n%s", view)
	}

	typeKeys(t, ptmx, "\x03")
	within(t, s.Context().Done(), "ctrl+c on the card after the hand-off")
	go func() {
		out, err := s.Ask(pageTwo())
		outs <- asked{out, err}
	}()
	waitView(t, s, "Set up git and the shell")
	typeKeys(t, ptmx, "\x1b")
	if got := within(t, outs, "esc on page 2"); got.out != Back {
		t.Errorf("esc on page 2 = %v, %v; want Back", got.out, got.err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
	raw = screen.waitFor(t, altLeave, 2)
	if enter, leaves := strings.Count(raw, altEnter), strings.Count(raw, altLeave); enter != 2 || leaves != 2 {
		t.Errorf("the walk entered the alternate screen %d times and left it %d times; want 2 and 2: once each for the walk and the hand-off", enter, leaves)
	}
}
