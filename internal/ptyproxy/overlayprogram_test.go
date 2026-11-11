// SPEC: _spec/internal/ptyproxy/pty-ownership.puml, _spec/internal/choiceui/tui-stack.puml
//go:build !windows

package ptyproxy

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// keyLog records key presses and quits on Enter.
type keyLog struct{ keys []string }

func (m keyLog) Init() tea.Cmd { return nil }

func (m keyLog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		if k.String() == "enter" {
			return m, tea.Quit
		}
		m.keys = append(m.keys, k.String())
	}
	return m, nil
}

func (m keyLog) View() tea.View { return tea.NewView("line one\nkeys: " + strings.Join(m.keys, " ")) }

// screenLog is a Program output a test reads once the Program ended.
type screenLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *screenLog) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *screenLog) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type programEnd struct {
	model tea.Model
	err   error
}

// startOverlayProgram runs a keyLog on a fresh hand-off reader.
func startOverlayProgram(t *testing.T) (chan []byte, *chanReader, *screenLog, <-chan programEnd) {
	t.Helper()
	feed := make(chan []byte, 16)
	r := &chanReader{ch: feed}
	out := &screenLog{}
	ended := make(chan programEnd, 1)
	go func() {
		m, err := overlayProgram(r, out, keyLog{}, tea.WithWindowSize(80, 24))
		ended <- programEnd{m, err}
	}()
	return feed, r, out, ended
}

func awaitProgram(t *testing.T, ended <-chan programEnd) programEnd {
	t.Helper()
	select {
	case e := <-ended:
		return e
	case <-time.After(10 * time.Second):
		t.Fatal("the overlay Program did not end")
		return programEnd{}
	}
}

func TestAnOverlayProgramTakesItsKeysFromTheHandOffAndHaltsItAtQuit(t *testing.T) {
	feed, r, _, ended := startOverlayProgram(t)
	feed <- []byte("n")
	feed <- []byte("\x1b[A")
	feed <- []byte("\r")
	end := awaitProgram(t, ended)
	if end.err != nil {
		t.Fatalf("Run: %v", end.err)
	}
	got, ok := end.model.(keyLog)
	if !ok {
		t.Fatalf("final model is %T, want the caller's own model", end.model)
	}
	if want := []string{"n", "up"}; strings.Join(got.keys, ",") != strings.Join(want, ",") {
		t.Errorf("keys = %v, want %v", got.keys, want)
	}
	if n, err := r.Read(make([]byte, 8)); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("the hand-off reader still reads after quit: %d, %v", n, err)
	}
	feed <- []byte("A")
	if rest := r.unread(); string(rest) != "A" {
		t.Errorf("a key typed after quit = %q, want it left for the child", rest)
	}
}

func TestAnOverlayProgramEndsWhenTheHandOffIsShut(t *testing.T) {
	_, r, _, ended := startOverlayProgram(t)
	time.Sleep(100 * time.Millisecond)
	r.shut()
	if end := awaitProgram(t, ended); !errors.Is(end.err, tea.ErrProgramKilled) {
		t.Errorf("Run = %v, want ErrProgramKilled once the child's exit shut the reader", end.err)
	}
}

// answering records key presses and has its answer at the first Enter, as a dialog does.
type answering struct {
	keys []string
	done bool
}

func (m answering) Init() tea.Cmd { return nil }
func (m answering) Done() bool    { return m.done }

func (m answering) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && !m.done {
		if k.String() == "enter" {
			m.done = true
			return m, tea.Quit
		}
		m.keys = append(m.keys, k.String())
	}
	return m, nil
}

func (m answering) View() tea.View { return tea.NewView("answering") }

// pause, as a chunk, is the operator not typing for longer than an escape sequence may take.
const pause = "\x00pause"

// runAnswering feeds chunks to an answering model and returns its keys and what went back to the hand-off.
func runAnswering(t *testing.T, chunks ...string) (keys []string, left string) {
	t.Helper()
	feed := make(chan []byte, 16)
	r := &chanReader{ch: feed}
	go func() {
		for _, c := range chunks {
			if c == pause {
				time.Sleep(3 * escWait)
				continue
			}
			feed <- []byte(c)
		}
	}()
	ended := make(chan programEnd, 1)
	go func() {
		m, err := overlayProgram(r, &screenLog{}, answering{}, tea.WithWindowSize(80, 24))
		ended <- programEnd{m, err}
	}()
	end := awaitProgram(t, ended)
	if end.err != nil {
		t.Fatalf("Run: %v", end.err)
	}
	time.Sleep(20 * time.Millisecond)
	return end.model.(answering).keys, string(r.unread())
}

func TestEveryByteAfterTheDecidingKeyGoesBackToTheChild(t *testing.T) {
	for _, c := range []struct {
		name     string
		chunks   []string
		keys     string
		wantLeft string
	}{
		{"the next key in its own chunk", []string{"n\r", "A"}, "n", "A"},
		{"the next key in the deciding key's chunk", []string{"n\rA"}, "n", "A"},
		{"a whole line typed ahead", []string{"\rls -la\r"}, "", "ls -la\r"},
		{"an escape sequence typed ahead", []string{"x\r\x1b[A"}, "x", "\x1b[A"},
		{"keys before the deciding one, in order", []string{"a", "b\x1b[Cc\r", "z"}, "a,b,right,c", "z"},
	} {
		keys, left := runAnswering(t, c.chunks...)
		if got := strings.Join(keys, ","); got != c.keys {
			t.Errorf("%s: the dialog saw %q, want %q", c.name, got, c.keys)
		}
		if left != c.wantLeft {
			t.Errorf("%s: %q went back to the child, want %q", c.name, left, c.wantLeft)
		}
	}
}

func TestAPasteNeverAnswersADialog(t *testing.T) {
	keys, left := runAnswering(t, "\x1b[200~ry\r\x1b[201~", "\r", "A")
	if len(keys) != 0 {
		t.Errorf("pasted text reached the dialog as keys: %v", keys)
	}
	if left != "A" {
		t.Errorf("%q went back to the child, want the key typed after the answer", left)
	}
}

func TestALoneEscapeIsAKeyAndASequenceIsOne(t *testing.T) {
	keys, _ := runAnswering(t, "\x1b", pause, "\x1b[", "A", "\x1b[27;5;99~", "\x1b[99;5u", "\r")
	if got, want := strings.Join(keys, ","), "esc,up,ctrl+c,ctrl+c"; got != want {
		t.Errorf("keys = %q, want %q", got, want)
	}
}

// childOwned are sequences that change a mode the child may have set, or ask the terminal a question.
var childOwned = map[string]string{
	"\x1b[>4;2m": "set modifyOtherKeys 2",
	"\x1b[>4m":   "reset modifyOtherKeys",
	"\x1b[>4;0m": "reset modifyOtherKeys",
	"\x1b[<":     "pop the kitty keyboard stack",
	"\x1b[?u":    "query the kitty keyboard flags",
	"\x1b[?2004": "bracketed paste",
	"\x1b[?1004": "focus reporting",
	"\x1b[?1000": "mouse reporting",
	"\x1b[?1002": "mouse reporting",
	"\x1b[?1003": "mouse reporting",
	"\x1b[?1006": "SGR mouse reporting",
	"$p":         "a DECRQM mode query",
	"\x1b]11;?":  "a background colour query",
	"\x1b[c":     "a device attributes query",
	"\x1b[>q":    "a terminal version query",
	"\x1b[6n":    "a cursor position query",
}

func TestAnOverlayProgramWritesNoSequenceTheChildOwns(t *testing.T) {
	feed, _, out, ended := startOverlayProgram(t)
	feed <- []byte("y")
	feed <- []byte("\r")
	if end := awaitProgram(t, ended); end.err != nil {
		t.Fatalf("Run: %v", end.err)
	}
	screen := out.String()
	for seq, what := range childOwned {
		if strings.Contains(screen, seq) {
			t.Errorf("the overlay wrote %q: %s", seq, what)
		}
	}
	if strings.Contains(screen, "\x1b[>") && strings.Contains(screen, "u") && strings.Contains(screen, "\x1b[>1u") {
		t.Errorf("the overlay pushed kitty keyboard flags")
	}
	for _, want := range []string{"\x1b[?1049h", "\x1b[?1049l", "line one", "keys: y"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the overlay never wrote %q", want)
		}
	}
}

// TestAProgramThatReadsItsOwnInputResetsTheChildsKeyboardMode records why the
// overlay decodes the hand-off itself instead of passing it as the Program's input.
func TestAProgramThatReadsItsOwnInputResetsTheChildsKeyboardMode(t *testing.T) {
	feed := make(chan []byte, 4)
	r := &chanReader{ch: feed}
	out := &screenLog{}
	prog := tea.NewProgram(overlayModel{inner: keyLog{}}, tea.WithInput(r), tea.WithOutput(out),
		tea.WithoutSignalHandler(), tea.WithWindowSize(80, 24))
	ended := make(chan error, 1)
	go func() {
		_, err := prog.Run()
		ended <- err
	}()
	feed <- []byte("\r")
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the Program did not end")
	}
	r.halt()
	screen := out.String()
	for _, seq := range []string{"\x1b[>4;2m", "\x1b[>4m"} {
		if !strings.Contains(screen, seq) {
			t.Errorf("bubbletea no longer writes %q with input on; the overlay's own decoding can be revisited", seq)
		}
	}
}

func TestOverlayOutputTurnsABareLineFeedIntoCarriageReturnLineFeed(t *testing.T) {
	for in, want := range map[string]string{
		"one\ntwo\n":     "one\r\ntwo\r\n",
		"one\r\ntwo":     "one\r\ntwo",
		"\n\n":           "\r\n\r\n",
		"no line feed":   "no line feed",
		"\x1b[2J\nframe": "\x1b[2J\r\nframe",
	} {
		if got := string(crlf([]byte(in))); got != want {
			t.Errorf("crlf(%q) = %q, want %q", in, got, want)
		}
	}
}
