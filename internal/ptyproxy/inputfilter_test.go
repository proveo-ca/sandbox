package ptyproxy

import (
	"testing"
	"time"
)

// The captured bytes from the run that produced a user message nobody typed.
// Zellij answered ONE Primary Device Attributes query twice, 42ms apart.
var (
	daReply      = []byte("\x1b[?62;4c")
	xtversion    = []byte("\x1bP>|Zellij(4301)\x1b\\")
	decrpm       = []byte("\x1b[?2026;2$y")
	focusIn      = []byte("\x1b[I")
	focusOut     = []byte("\x1b[O")
	cursorReport = []byte("\x1b[24;80R")
)

func TestClassifyTerminalReport(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   []byte
		want reportKind
	}{
		{"device attributes", daReply, reportReply},
		{"secondary device attributes", []byte("\x1b[>0;276;0c"), reportReply},
		{"xtversion DCS", xtversion, reportReply},
		{"mode report", decrpm, reportReply},
		{"cursor position", cursorReport, reportReply},
		{"osc colour reply", []byte("\x1b]11;rgb:0000/0000/0000\x07"), reportReply},
		{"focus in", focusIn, reportFocus},
		{"focus out", focusOut, reportFocus},

		// Keystrokes must never be mistaken for reports.
		{"plain text", []byte("say hello"), reportNone},
		{"carriage return", []byte("\r"), reportNone},
		{"up arrow", []byte("\x1b[A"), reportNone},
		{"application up arrow", []byte("\x1bOA"), reportNone},
		{"home key", []byte("\x1b[H"), reportNone},
		{"alt-c", []byte("\x1bc"), reportNone},
		{"bare escape", []byte("\x1b"), reportNone},
		{"shift-tab", []byte("\x1b[Z"), reportNone},
		{"bracketed paste start", []byte("\x1b[200~"), reportNone},
		// A report bundled with anything else is NOT a lone report: forwarding it
		// whole is the only way to be sure no keystroke is eaten.
		{"report plus keystroke", append(append([]byte{}, daReply...), 'x'), reportNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyTerminalReport(tc.in); got != tc.want {
				t.Errorf("classifyTerminalReport(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// The first answer is owed to the application that asked; only the surplus is
// dropped. Getting this backwards would leave the app waiting forever.
func TestFilterForwardsFirstReplyDropsDuplicate(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	f := newInputFilter()
	f.now = func() time.Time { return now }

	if !f.keep(daReply) {
		t.Fatal("the first DA reply must reach the application that queried")
	}
	now = now.Add(42 * time.Millisecond) // the observed Zellij interval
	if f.keep(daReply) {
		t.Error("the duplicate DA reply must be dropped")
	}
	// Far enough apart to be a genuine second query, not a relay artifact.
	now = now.Add(DefaultReplyWindow + time.Second)
	if !f.keep(daReply) {
		t.Error("a reply outside the window answers a new query and must pass")
	}
}

// Two DIFFERENT reports in a row are two different answers.
func TestFilterDoesNotConflateDistinctReplies(t *testing.T) {
	t.Parallel()
	f := newInputFilter()
	for _, b := range [][]byte{xtversion, daReply, decrpm, cursorReport} {
		if !f.keep(b) {
			t.Errorf("distinct reply %q was dropped", b)
		}
	}
}

func TestFilterDropsFocusEvents(t *testing.T) {
	t.Parallel()
	f := newInputFilter()
	if f.keep(focusIn) || f.keep(focusOut) {
		t.Error("focus reports are neither keystrokes nor answers; they must not reach the agent")
	}
	f.dropFocus = false
	if !f.keep(focusIn) {
		t.Error("dropFocus=false must forward focus reports")
	}
}

// The filter exists to protect input, so it must never cost a keystroke.
func TestFilterNeverDropsKeystrokes(t *testing.T) {
	t.Parallel()
	f := newInputFilter()
	for _, b := range [][]byte{
		[]byte("s"), []byte("h"), []byte("\r"), []byte("say hello"),
		[]byte("\x1b[A"), []byte("\x1bOB"), []byte("\x03"), []byte("\x04"),
		[]byte("\x1b[200~pasted\x1b[201~"),
	} {
		if !f.keep(b) {
			t.Errorf("keystroke %q was dropped", b)
		}
		if !f.keep(b) {
			t.Errorf("repeated keystroke %q was dropped as a duplicate reply", b)
		}
	}
}

// A byte-for-byte replay of the captured trace from proveo-1787703005-59852,
// the run that ended with the agent answering a prompt nobody typed.
func TestReplayOfTheCapturedZellijTrace(t *testing.T) {
	reads := [][]byte{
		[]byte("\x1bP>|Zellij(4301)\x1b\\"), // XTVERSION reply
		[]byte("\x1b[?62;4c"),               // DA reply
		[]byte("\x1b[?2026;2$y"),            // DECRQM reply
		[]byte("\x1b[?62;4c"),               // DA reply AGAIN — Zellij's surplus
		[]byte("s"), []byte("a"), []byte("y"), []byte(" "),
		[]byte("h"), []byte("e"), []byte("l"), []byte("l"), []byte("o"),
		[]byte("\r"),
		[]byte("\x1b[O"), // focus out
		[]byte("\x1b[I"), // focus in
	}
	want := []string{
		"\x1bP>|Zellij(4301)\x1b\\", "\x1b[?62;4c", "\x1b[?2026;2$y",
		"s", "a", "y", " ", "h", "e", "l", "l", "o", "\r",
	}

	f := newInputFilter()
	var got []string
	for _, r := range reads {
		if f.keep(r) {
			got = append(got, string(r))
		}
	}
	if len(got) != len(want) {
		t.Fatalf("forwarded %d reads, want %d:\n got %q\nwant %q", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("read %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A far end that reads its input as a prompt stream never queried anything, so
// no report is owed to it and the FIRST copy is already one too many. The dedup
// rule cannot express that: it forwards the first copy by construction.
func TestDropRepliesRemovesEvenAnUnpairedReport(t *testing.T) {
	t.Parallel()
	f := newInputFilter()
	f.dropReplies = true

	for _, b := range [][]byte{daReply, xtversion, decrpm, cursorReport, focusIn, focusOut} {
		if f.keep(b) {
			t.Errorf("report %q reached a prompt stream that never asked for it", b)
		}
	}
	// Twice, because the dedup path forwards a first copy and this must not be
	// reachable at all — not merely shadowed by the memory of a previous read.
	if f.keep(daReply) {
		t.Error("the first copy is the one that killed the run; it must not pass either")
	}
}

// The knob must not cost a keystroke: dropping input to win a filtering
// argument is the one outcome worse than forwarding a stray report.
func TestDropRepliesStillNeverDropsKeystrokes(t *testing.T) {
	t.Parallel()
	f := newInputFilter()
	f.dropReplies = true
	for _, b := range [][]byte{
		[]byte("s"), []byte("\r"), []byte("say hello"), []byte("\x03"),
		[]byte("\x1b[A"), []byte("\x1bOB"), []byte("\x1b[200~pasted\x1b[201~"),
	} {
		if !f.keep(b) {
			t.Errorf("keystroke %q was dropped", b)
		}
	}
}

// The exact read that killed proveo-1787852436-14907: ONE VT102 Device
// Attributes reply, five seconds into a run, with no duplicate to mark it as
// surplus and nothing on the sbx side that had queried. Under the default rule
// it is forwarded — which is the bug, so the default is asserted here too.
func TestTheLoneReplyThatKilledTheSbxRun(t *testing.T) {
	t.Parallel()
	lone := []byte("\x1b[?6c")

	if got := classifyTerminalReport(lone); got != reportReply {
		t.Fatalf("classify(%q) = %v, want reportReply", lone, got)
	}
	if !newInputFilter().keep(lone) {
		t.Error("default rule: the first copy is owed to an application that asked; that is the docker contract")
	}
	sbx := newInputFilter()
	sbx.dropReplies = true
	if sbx.keep(lone) {
		t.Error("sbx: nothing queried, so this must never reach the prompt stream")
	}
}

var (
	mouseSGRMotion  = []byte("\x1b[<35;1;46M")
	mouseSGRPress   = []byte("\x1b[<0;12;7M")
	mouseSGRRelease = []byte("\x1b[<0;12;7m")
	mouseURXVT      = []byte("\x1b[35;1;46M")
	mouseX10        = []byte("\x1b[M\x20\x21\x22")
)

func TestClassifyRecognisesEveryMouseEncoding(t *testing.T) {
	t.Parallel()
	for _, b := range [][]byte{mouseSGRMotion, mouseSGRPress, mouseSGRRelease, mouseURXVT, mouseX10} {
		if got := classifyTerminalReport(b); got != reportMouse {
			t.Errorf("classify(%q) = %v, want reportMouse", b, got)
		}
	}
}

func TestMouseReportsSurviveOnATTYAndAreDroppedOnAPromptStream(t *testing.T) {
	t.Parallel()
	tty := newInputFilter()
	for i, b := range [][]byte{mouseSGRMotion, mouseSGRMotion, mouseSGRPress, mouseSGRRelease} {
		if !tty.keep(b) {
			t.Errorf("read %d (%q) was dropped; a TUI on a real tty consumes clicks and scroll", i, b)
		}
	}

	stream := newInputFilter()
	stream.dropReplies = true
	for _, b := range [][]byte{mouseSGRMotion, mouseSGRPress, mouseSGRRelease, mouseURXVT, mouseX10} {
		if stream.keep(b) {
			t.Errorf("mouse report %q reached a prompt stream that cannot consume one", b)
		}
	}
}

func TestMouseMotionIsNeverDeduplicated(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	f := newInputFilter()
	f.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if !f.keep(mouseSGRMotion) {
			t.Fatalf("identical motion report %d was dropped as a duplicate; a mouse held still still reports", i)
		}
		now = now.Add(3 * time.Millisecond)
	}
}

func TestReplayOfTheCapturedMouseTrace(t *testing.T) {
	t.Parallel()
	reads := [][]byte{
		[]byte("\x1b[?6c"),
		[]byte("\x1b[I"),
		[]byte("\x1b[<35;1;46M"), []byte("\x1b[<35;2;45M"), []byte("\x1b[<35;3;45M"),
		[]byte("/"), []byte("c"), []byte("o"), []byte("l"), []byte("o"), []byte("r"),
		[]byte(" "), []byte("r"), []byte("e"), []byte("d"), []byte("\r"),
	}
	want := []string{"/", "c", "o", "l", "o", "r", " ", "r", "e", "d", "\r"}

	f := newInputFilter()
	f.dropReplies = true
	var got []string
	for _, r := range reads {
		if f.keep(r) {
			got = append(got, string(r))
		}
	}
	if len(got) != len(want) {
		t.Fatalf("forwarded %d reads, want %d (keystrokes only):\n got %q\nwant %q", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("read %d = %q, want %q", i, got[i], want[i])
		}
	}
}
