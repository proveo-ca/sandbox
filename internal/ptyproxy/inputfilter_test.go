package ptyproxy

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
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

// SPEC: _spec/internal/ptyproxy/terminal-report-filter.puml
func TestKittyRepliesAreNotKeystrokes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		b    []byte
	}{
		{"kitty graphics answer (APC)", []byte("\x1b_Gi=31337;OK\x1b\\")},
		{"kitty graphics error (APC)", []byte("\x1b_Gi=31337;ENOTSUPPORTED\x1b\\")},
		{"kitty keyboard flags (CSI ? u)", []byte("\x1b[?5u")},
		{"kitty keyboard flags, zero", []byte("\x1b[?0u")},
	} {
		if got := classifyTerminalReport(tc.b); got != reportReply {
			t.Errorf("%s: classified %v, want reportReply — it reaches the agent as input",
				tc.name, got)
		}
	}
}

func TestRealKeystrokesStillPassTheFilter(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		b    []byte
	}{
		{"kitty-encoded keypress", []byte("\x1b[1;5u")}, // no '?' — a key, not a report
		{"F7", []byte("\x1b[18~")},
		{"F8", []byte("\x1b[19~")},
		{"plain letter c", []byte("c")},
		{"arrow up", []byte("\x1b[A")},
		{"APC that never terminates", []byte("\x1b_Gi=1;OK")},
	} {
		if got := classifyTerminalReport(tc.b); got != reportNone {
			t.Errorf("%s: classified %v, want reportNone — the agent must still see it",
				tc.name, got)
		}
	}
}

// SPEC: _spec/internal/ptyproxy/terminal-report-filter.puml
func TestASplitReplyDoesNotLeakItsTail(t *testing.T) {
	t.Parallel()
	f := newInputFilter()
	f.dropReplies = true // the sbx path sets DropReports

	// Read 1 ends mid-reply: the head must be HELD, not forwarded.
	fwd, held := f.split([]byte("hello\x1b[?62;"))
	if string(fwd) != "hello" {
		t.Errorf("forwarded %q, want the real keystrokes only", fwd)
	}
	if string(held) != "\x1b[?62;" {
		t.Errorf("held %q, want the partial reply carried forward", held)
	}
	// Read 2 completes it. Rejoined, it is a reply and is dropped whole.
	fwd, held = f.split(append(held, []byte("c")...))
	if len(fwd) != 0 {
		t.Errorf("forwarded %q — the reply tail reached the agent as input", fwd)
	}
	if len(held) != 0 {
		t.Errorf("still holding %q after the reply completed", held)
	}
}

func TestAPasteSurvivesAReportInTheSameRead(t *testing.T) {
	t.Parallel()
	f := newInputFilter()
	f.dropReplies = true

	paste := "\x1b[200~sk-REDACTED\x1b[201~"
	fwd, held := f.split([]byte(paste + "\x1b[?62;1c"))
	if string(fwd) != paste {
		t.Errorf("forwarded %q, want the paste intact and the reply gone", fwd)
	}
	if len(held) != 0 {
		t.Errorf("held %q, want nothing — the reply was complete", held)
	}
}

func TestUnfinishedEscapesDoNotStallInput(t *testing.T) {
	t.Parallel()
	f := newInputFilter()
	f.dropReplies = true

	fwd, held := f.split([]byte{0x1b})
	if len(fwd) != 0 || string(held) != "\x1b" {
		t.Errorf("split(ESC) = %q, %q; want it held for one read", fwd, held)
	}
	long := append([]byte{0x1b, '['}, bytes.Repeat([]byte("1;"), maxHeld)...)
	fwd, held = f.split(long)
	if len(held) != 0 {
		t.Errorf("held %d bytes; beyond maxHeld it must be released, not hoarded", len(held))
	}
	if len(fwd) != len(long) {
		t.Errorf("forwarded %d of %d bytes — real input was dropped", len(fwd), len(long))
	}
}

// Ordinary typing must be untouched by any of this.
func TestPlainTypingPassesThroughUnchanged(t *testing.T) {
	t.Parallel()
	f := newInputFilter()
	f.dropReplies = true
	for _, in := range []string{"ls -la\r", "y", "\x03", "git commit -m 'x'\n"} {
		fwd, held := f.split([]byte(in))
		if string(fwd) != in || len(held) != 0 {
			t.Errorf("split(%q) = %q, held %q; want it verbatim", in, fwd, held)
		}
	}
}

// The application announces mouse tracking on its OUTPUT stream; the filter
// reads that to decide whether an incoming mouse report was asked for.
// SPEC: _spec/internal/ptyproxy/terminal-report-filter.puml
func TestMouseTrackingFollowsTheChildsModeSets(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		reads []string
		want  bool
	}{
		{"no mode sets at all", []string{"hello world"}, false},
		{"1000 click tracking", []string{"\x1b[?1000h"}, true},
		{"1002 button drag", []string{"\x1b[?1002h"}, true},
		{"1003 any motion", []string{"\x1b[?1003h"}, true},
		{"multi-param set in one sequence", []string{"\x1b[?1002;1006h"}, true},
		{"an encoding alone reports nothing", []string{"\x1b[?1006h"}, false},
		{"bracketed paste is not mouse", []string{"\x1b[?2004h"}, false},
		{"alt screen is not mouse", []string{"\x1b[?1049h"}, false},
		{"enabled then disabled", []string{"\x1b[?1002h", "\x1b[?1002l"}, false},
		{"one of two disabled leaves tracking on", []string{"\x1b[?1000;1002h", "\x1b[?1002l"}, true},
		{"params split across two reads", []string{"\x1b[?10", "02h"}, true},
		{"escape split from its body", []string{"paint\x1b", "[?1003h"}, true},
		{"final byte split off", []string{"\x1b[?1003", "h"}, true},
		{"disable split across two reads", []string{"\x1b[?1003h", "\x1b[?100", "3l"}, false},
		{"buried in a repaint", []string{"hi\x1b[2J\x1b[?1002h\x1b[H"}, true},
		{"opencode's start-up bundle", []string{"\x1b[?1049h\x1b[?1002h\x1b[?1006h"}, true},
		{"and its shutdown bundle", []string{"\x1b[?1049h\x1b[?1002h\x1b[?1006h", "\x1b[?1002l\x1b[?1006l\x1b[?1049l"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newInputFilter()
			for _, r := range tc.reads {
				f.mouse.observe([]byte(r))
			}
			if got := f.mouse.enabled(); got != tc.want {
				t.Errorf("observe(%q): mouse.enabled() = %v, want %v", tc.reads, got, tc.want)
			}
		})
	}
}

// The reported bug: opencode turns tracking on, so a drag is input it asked
// for and must reach it even on the sbx path that drops unasked-for reports.
func TestMouseInputReachesAnAgentThatTurnedTrackingOn(t *testing.T) {
	t.Parallel()
	f := newInputFilter()
	f.dropReplies = true // what sandbox.go's DropReports sets
	f.mouse.observe([]byte("\x1b[?1002;1006h"))

	for _, tc := range []struct {
		name string
		b    []byte
	}{
		{"press", mouseSGRPress},
		{"release", mouseSGRRelease},
		{"drag motion", mouseSGRMotion},
		{"wheel up", []byte("\x1b[<64;10;5M")},
		{"wheel down", []byte("\x1b[<65;10;5M")},
		{"urxvt encoding", mouseURXVT},
		{"x10 encoding", mouseX10},
	} {
		if !f.keep(tc.b) {
			t.Errorf("keep(%q) = false for a %s while tracking is on; nothing in the TUI is selectable", tc.b, tc.name)
		}
	}
	// split() is the real entry point: a drag arriving mid-read must survive too.
	fwd, held := f.split([]byte("\x1b[<32;10;5M"))
	if string(fwd) != "\x1b[<32;10;5M" || len(held) != 0 {
		t.Errorf("split(drag) = %q, held %q; want the drag forwarded whole", fwd, held)
	}
}

// With tracking off the captured trace must filter exactly as before: that is
// the behaviour TestReplayOfTheCapturedMouseTrace pins.
func TestCapturedMouseTraceStaysFilteredWhileTrackingIsOff(t *testing.T) {
	t.Parallel()
	f := newInputFilter()
	f.dropReplies = true
	// Output the child really wrote, none of which starts mouse reporting.
	f.mouse.observe([]byte("\x1b[?1049h\x1b[?2004h\x1b[?1006h\x1b[?25l"))

	reads := [][]byte{
		[]byte("\x1b[?6c"),
		[]byte("\x1b[I"),
		[]byte("\x1b[<35;1;46M"), []byte("\x1b[<35;2;45M"), []byte("\x1b[<35;3;45M"),
		[]byte("/"), []byte("c"), []byte("o"), []byte("l"), []byte("o"), []byte("r"),
		[]byte(" "), []byte("r"), []byte("e"), []byte("d"), []byte("\r"),
	}
	want := []string{"/", "c", "o", "l", "o", "r", " ", "r", "e", "d", "\r"}

	var got []string
	for _, r := range reads {
		if f.keep(r) {
			got = append(got, string(r))
		}
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("replay of the captured trace with tracking off mismatch (-want +got):\n%s", diff)
	}
}

// Tracking turned off again must stop forwarding: an app that exits its TUI
// leaves the prompt stream it was the whole point of the drop knob.
func TestMouseForwardingStopsWhenTheAppTurnsTrackingOff(t *testing.T) {
	t.Parallel()
	f := newInputFilter()
	f.dropReplies = true
	f.mouse.observe([]byte("\x1b[?1002h"))
	if !f.keep(mouseSGRMotion) {
		t.Errorf("keep(%q) = false while tracking is on", mouseSGRMotion)
	}
	f.mouse.observe([]byte("\x1b[?1002l"))
	if f.keep(mouseSGRMotion) {
		t.Errorf("keep(%q) = true after the app turned tracking off", mouseSGRMotion)
	}
}

// A run of output that never completes a mode set must not be hoarded.
func TestMouseTrackerDoesNotHoardUnterminatedOutput(t *testing.T) {
	t.Parallel()
	f := newInputFilter()
	f.mouse.observe(append([]byte("\x1b["), bytes.Repeat([]byte("1;"), maxModeCarry)...))
	f.mouse.mu.Lock()
	held := len(f.mouse.scan.carry)
	f.mouse.mu.Unlock()
	if held != 0 {
		t.Errorf("carried %d bytes of child output; beyond maxModeCarry it must be dropped", held)
	}
}

// SPEC: _spec/internal/ptyproxy/terminal-report-filter.puml
func TestSolicitedCPRSurvivesTheBlanketDrop(t *testing.T) {
	f := newInputFilter()
	f.dropReplies = true // the sbx backend
	cpr := []byte("\x1b[24;80R")

	// Unasked-for: this is the case dropReplies exists for — proveo's own
	// overlay provokes reports the agent never wanted.
	if f.keep(cpr) {
		t.Error("an unsolicited cursor position report reached the agent")
	}

	// The child asks on its OWN output stream, which is the only place the
	// asking is visible.
	f.cpr.observe([]byte("\x1b[6n"))
	if !f.keep(cpr) {
		t.Error("the child sent CSI 6n and is blocked waiting; withholding the answer " +
			"is what makes cecli report 'your terminal doesn't support cursor position " +
			"requests (CPR)' and then block startup on a Y/N prompt")
	}
	// One query, one answer. The credit must not persist.
	if f.keep(cpr) {
		t.Error("a single query licensed two reports — the second was unsolicited")
	}
}

func TestOnlyDSR6IsACursorQuery(t *testing.T) {
	for _, c := range []struct {
		name  string
		query string
		want  bool
	}{
		{"DSR-CPR", "\x1b[6n", true},
		{"DECXCPR", "\x1b[?6n", true},
		{"device status is not a cursor query", "\x1b[5n", false},
		{"a mode set is not a query", "\x1b[?1002h", false},
		{"not a final n", "\x1b[6m", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newInputFilter()
			f.dropReplies = true
			f.cpr.observe([]byte(c.query))
			if got := f.keep([]byte("\x1b[1;1R")); got != c.want {
				t.Errorf("after %q, keep(CPR)=%v, want %v", c.query, got, c.want)
			}
		})
	}
}

// The credit is per-report-class: asking for the cursor must not licence a
// device-attributes answer, which nothing asked for.
func TestACursorQueryDoesNotLicenceOtherReports(t *testing.T) {
	f := newInputFilter()
	f.dropReplies = true
	f.cpr.observe([]byte("\x1b[6n"))
	if f.keep([]byte("\x1b[?62;1c")) {
		t.Error("a cursor query let a Device Attributes reply through")
	}
}

// A query split across two child writes must still be seen: the scanner holds
// the partial exactly as the mouse watch does.
func TestASplitCursorQueryIsStillSeen(t *testing.T) {
	f := newInputFilter()
	f.dropReplies = true
	f.cpr.observe([]byte("painting…\x1b[6"))
	f.cpr.observe([]byte("n more paint"))
	if !f.keep([]byte("\x1b[24;80R")) {
		t.Error("a CSI 6n split across two writes was missed")
	}
}

func TestOutstandingCursorQueriesAreBounded(t *testing.T) {
	f := newInputFilter()
	f.dropReplies = true
	for i := 0; i < maxOutstandingCPR*4; i++ {
		f.cpr.observe([]byte("\x1b[6n"))
	}
	kept := 0
	for i := 0; i < maxOutstandingCPR*4; i++ {
		if f.keep([]byte("\x1b[1;1R")) {
			kept++
		}
	}
	if kept > maxOutstandingCPR {
		t.Errorf("kept %d reports; a child that queries and never reads must not build "+
			"an unbounded licence to forward stray reports (cap %d)", kept, maxOutstandingCPR)
	}
}
