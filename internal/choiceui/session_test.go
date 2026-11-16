// SPEC: _spec/internal/choiceui/session.puml
package choiceui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/proveo-ca/proveo/internal/ui"
)

// tape records, in order, what a hand-off wrote and when the fake Exec released and restored.
type tape struct {
	mu     sync.Mutex
	events []string
}

func (t *tape) Write(p []byte) (int, error) {
	t.note("write " + plain(string(p)))
	return len(p), nil
}

func (t *tape) note(e string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, e)
}

func (t *tape) all() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.events...)
}

// driven is a session model fed by hand: a fake Exec in place of Bubble Tea's, a clock the test
// moves, and the context ctrl+c cancels.
type driven struct {
	t     *testing.T
	m     tea.Model
	ctx   context.Context
	tape  *tape
	clock time.Time
}

func newDriven(t *testing.T, w, h int) *driven {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := &driven{t: t, ctx: ctx, tape: &tape{}, clock: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)}
	sm := newSessionModel(d.tape, cancel)
	sm.now = func() time.Time { return d.clock }
	sm.exec = func(c tea.ExecCommand, fn tea.ExecCallback) tea.Cmd {
		return func() tea.Msg {
			d.tape.note("release")
			err := c.Run()
			d.tape.note("restore")
			return fn(err)
		}
	}
	d.m, _ = sm.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return d
}

// send gives the model msg and runs the command it returns, once, when that is a hand-off.
func (d *driven) send(msg sessionMsg) {
	var cmd tea.Cmd
	d.m, cmd = d.m.Update(msg)
	if _, hand := msg.(handMsg); hand && cmd != nil {
		d.m, _ = d.m.Update(cmd())
	}
}

func (d *driven) press(keys ...tea.KeyPressMsg) {
	for _, k := range keys {
		d.m, _ = d.m.Update(k)
	}
}

func (d *driven) ask(f *Form) chan Outcome {
	reply := make(chan Outcome, 1)
	d.send(askMsg{f: f, reply: reply})
	return reply
}

func (d *driven) view() string { return plain(d.m.View().Content) }

func (d *driven) stopped() bool { return d.ctx.Err() != nil }

// got is the answer on c, or fails when none came.
func got[T any](t *testing.T, c chan T, what string) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	default:
		var zero T
		t.Fatalf("%s: no answer", what)
		return zero
	}
}

// none fails when an answer came on c.
func none[T any](t *testing.T, c chan T, what string) {
	t.Helper()
	select {
	case v := <-c:
		t.Fatalf("%s: answered %v, want no answer yet", what, v)
	default:
	}
}

// running is page 1 after Enter, with Docker login as the plan.
func running(t *testing.T, d *driven) *Form {
	t.Helper()
	f := pageOne(keepAsIs)
	reply := d.ask(f)
	d.press(teaEnter)
	if out := got(t, reply, "enter on page 1"); out != Accepted {
		t.Fatalf("enter on page 1 = %v, want Accepted", out)
	}
	d.send(planMsg{labels: []string{"Docker login"}})
	d.send(statusMsg{label: "Docker login", mark: MarkRunning, outcome: "sbx login · Docker not signed in"})
	return f
}

func TestASessionKeepsOneProgramAcrossAsks(t *testing.T) {
	d := newDriven(t, 80, 24)
	if !d.m.View().AltScreen {
		t.Fatal("the session drew its first frame off the alternate screen")
	}
	one := d.ask(pageOne(keepAsIs))
	if v := d.m.View(); !v.AltScreen || !strings.Contains(plain(v.Content), "Prepare sbx on this") {
		t.Fatalf("page 1 is not up on the alternate screen:\n%s", plain(v.Content))
	}
	d.press(teaEnter)
	if out := got(t, one, "enter"); out != Accepted {
		t.Fatalf("enter = %v, want Accepted", out)
	}
	if v := d.m.View(); !v.AltScreen || !strings.Contains(plain(v.Content), "Prepare sbx on this") {
		t.Fatalf("an accepted page left the screen before the next ask:\n%s", plain(v.Content))
	}
	two := d.ask(pageTwo())
	if v := d.m.View(); !v.AltScreen || !strings.Contains(plain(v.Content), "Set up git and the shell") {
		t.Fatalf("page 2 did not replace page 1 on the alternate screen:\n%s", plain(v.Content))
	}
	d.press(teaEsc)
	if out := got(t, two, "esc on page 2"); out != Back {
		t.Errorf("esc on page 2 = %v, want Back", out)
	}
	if !d.m.View().AltScreen {
		t.Error("the session left the alternate screen between pages")
	}
}

// A row an answer opened belongs to that answer: once the run starts it reads its answer with no
// mark, unless the plan runs it.
func TestARowAnAnswerOpenedTakesNoMarkUnlessItRuns(t *testing.T) {
	for _, c := range []struct {
		form  *Form
		plan  []string
		marks map[string]Mark
		bare  string
	}{
		{pageTwo(), []string{"git identity"}, map[string]Mark{"git identity": MarkAhead, "user.name": MarkNone, "user.email": MarkNone}, "user.name"},
		{pageOne("rootless tarball"), []string{"sbx install", "PATH", "Docker login"},
			map[string]Mark{"sbx install": MarkAhead, "location": MarkNone, "PATH": MarkAhead}, "location"},
	} {
		d := newDriven(t, 120, 36)
		reply := d.ask(c.form)
		d.press(teaEnter)
		got(t, reply, "enter on "+c.form.Title)
		d.send(planMsg{labels: c.plan})
		for _, r := range c.form.Rows {
			if want, ok := c.marks[r.Label]; ok && r.Mark != want {
				t.Errorf("%s: %s reads mark %v, want %v", c.form.Title, r.Label, r.Mark, want)
			}
		}
		if view := d.view(); !regexp.MustCompile(`│ {5}` + regexp.QuoteMeta(c.bare) + ` `).MatchString(view) {
			t.Errorf("%s: %s does not draw bare:\n%s", c.form.Title, c.bare, view)
		}
	}
}

func TestARunningFormTakesOnlyCtrlC(t *testing.T) {
	d := newDriven(t, 80, 24)
	f := running(t, d)
	before := d.view()
	d.press(teaKey(tea.KeyDown, 0), teaKey(tea.KeyUp, 0), teaRight, teaLeft, teaTab, teaEnter, teaEsc)
	d.press(typed("q jk")...)
	if d.stopped() {
		t.Fatal("a key other than ctrl+c stopped the run")
	}
	if after := d.view(); after != before {
		t.Errorf("a key changed the running page:\nbefore\n%s\nafter\n%s", before, after)
	}
	if f.Selection("sbx install") != keepAsIs || f.Progress.Stopping {
		t.Errorf("a key reached the running form: sbx install = %q, stopping = %v", f.Selection("sbx install"), f.Progress.Stopping)
	}
	d.press(teaCtrlC)
	if !d.stopped() {
		t.Fatal("ctrl+c while running did not cancel the context")
	}
	if !strings.Contains(d.view(), "stopping after Docker login …") {
		t.Errorf("the key line does not say the run stops after its row:\n%s", d.view())
	}
}

func TestStepsUpdateTheirRowsWhileTheyRun(t *testing.T) {
	d := newDriven(t, 80, 24)
	f := pageOne(keepAsIs)
	reply := d.ask(f)
	d.press(teaEnter)
	got(t, reply, "enter")
	d.send(planMsg{labels: []string{"sbx install", "Docker login"}})
	view := d.view()
	for _, want := range []string{"· sbx install   keep as is", "· Docker login  now", "running 1 of 2 — sbx install · ctrl+c stop"} {
		if !strings.Contains(view, want) {
			t.Errorf("after the plan the card lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "(•)") {
		t.Errorf("a planned page still draws answer cells:\n%s", view)
	}

	d.send(statusMsg{label: "sbx install", mark: MarkLater, outcome: "kept 0.50.0"})
	d.send(statusMsg{label: "Docker login", mark: MarkRunning, outcome: "sbx login"})
	view = d.view()
	for _, want := range []string{"– sbx install   kept 0.50.0", "◓ Docker login  sbx login", "running 2 of 2 — Docker login"} {
		if !strings.Contains(view, want) {
			t.Errorf("a running row lacks %q:\n%s", want, view)
		}
	}
	d.clock = d.clock.Add(time.Second)
	d.m, _ = d.m.Update(formTick{})
	if strings.Contains(d.view(), "sbx login · ") {
		t.Errorf("the clock showed before two seconds:\n%s", d.view())
	}
	d.clock = d.clock.Add(11 * time.Second)
	d.m, _ = d.m.Update(formTick{})
	if !strings.Contains(d.view(), "Docker login  sbx login · 12s") {
		t.Errorf("a row running 12s does not say so:\n%s", d.view())
	}
	d.send(statusMsg{label: "Docker login", mark: MarkDone, outcome: "signed in"})
	d.clock = d.clock.Add(5 * time.Second)
	d.m, _ = d.m.Update(formTick{})
	if view := d.view(); !strings.Contains(view, "✓ Docker login  signed in") || strings.Contains(view, "signed in · ") {
		t.Errorf("a finished row keeps a clock or lacks its outcome:\n%s", view)
	}
}

// handErr is a hand-off's error that knows whether a Ctrl+C ended the program.
type handErr struct{ interrupted bool }

func (e handErr) Error() string { return "exit status 130" }

func TestHandReleasesPrintsTheFrameAndRestores(t *testing.T) {
	d := newDriven(t, 80, 24)
	f := running(t, d)
	reply := make(chan error, 1)
	d.send(handMsg{
		command: "sbx login",
		parts:   []string{"Docker not signed in", "sbx's screen", "page 1/4 returns when it exits"},
		run: func() error {
			d.tape.note("run")
			return handErr{interrupted: true}
		},
		outcome: func(err error) (Mark, string) {
			if err != nil {
				return MarkNeeds, "stopped · Ctrl+C ended it"
			}
			return MarkDone, "signed in"
		},
		reply: reply,
	})
	err := got(t, reply, "hand")
	var hand handErr
	if !errors.As(err, &hand) || !hand.interrupted {
		t.Errorf("Hand returned %v, want run's own error with its interrupted flag", err)
	}
	frame := ui.FrameLine(80, "sbx login", "Docker not signed in", "sbx's screen", "page 1/4 returns when it exits")
	if !strings.HasPrefix(frame, "── sbx login ── sbx's screen · page 1/4 returns on exit ──") {
		t.Fatalf("the 80-column frame is not the spec's: %q", frame)
	}
	want := []string{
		"release",
		"write \n" + frame + "\n",
		"run",
		"write \n",
		"write   ! Docker login  stopped · Ctrl+C ended it\n",
		"restore",
	}
	if got := d.tape.all(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("hand-off order:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(d.view(), "! Docker login  stopped · Ctrl+C ended it") {
		t.Errorf("the card came back without the outcome on its row:\n%s", d.view())
	}
	if d.stopped() || f.Progress.Stopping {
		t.Error("a hand-off with no SIGINT stopped the run")
	}
}

func TestTheCardComesBackWithTheHandOffsOutcome(t *testing.T) {
	d := newDriven(t, 80, 24)
	running(t, d)
	reply := make(chan error, 1)
	var cmd tea.Cmd
	d.m, cmd = d.m.Update(handMsg{
		command: "sbx login",
		run: func() error {
			d.clock = d.clock.Add(30 * time.Second)
			return nil
		},
		outcome: func(error) (Mark, string) { return MarkDone, "signed in" },
		reply:   reply,
	})
	back := cmd()
	d.m, _ = d.m.Update(formTick{})
	if view := d.view(); !strings.Contains(view, "✓ Docker login  signed in") || strings.Contains(view, "30s") {
		t.Errorf("the card came back without the hand-off's outcome, or the clock counted the hand-off:\n%s", view)
	}
	d.m, _ = d.m.Update(back)
	if err := got(t, reply, "hand"); err != nil {
		t.Fatalf("Hand = %v", err)
	}
	d.clock = d.clock.Add(5 * time.Second)
	d.m, _ = d.m.Update(formTick{})
	if view := d.view(); !strings.Contains(view, "✓ Docker login  signed in") || strings.Contains(view, "signed in · ") {
		t.Errorf("the row lost its outcome after the hand-off answered:\n%s", view)
	}
}

func TestAHandOffIsPlainWhenTheStreamIsPlain(t *testing.T) {
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	for _, plain := range []bool{true, false} {
		t.Run(map[bool]string{true: "plain", false: "fancy"}[plain], func(t *testing.T) {
			ui.Default = ui.NewTerminal(io.Discard, 80)
			if plain {
				ui.Default = ui.New(&bytes.Buffer{})
			}
			d := newDriven(t, 80, 24)
			running(t, d)
			var raw bytes.Buffer
			sm := d.m.(sessionModel)
			sm.out = &raw
			d.m = sm
			reply := make(chan error, 1)
			d.send(handMsg{command: "sbx login", parts: []string{"sbx's screen"}, run: func() error { return nil },
				outcome: func(error) (Mark, string) { return MarkDone, "signed in" }, reply: reply})
			if err := got(t, reply, "hand"); err != nil {
				t.Fatalf("Hand = %v", err)
			}
			if !strings.Contains(raw.String(), "Docker login  signed in") {
				t.Fatalf("the hand-off printed no outcome row: %q", raw.String())
			}
			if escapes := strings.Contains(raw.String(), "\x1b["); escapes == plain {
				t.Errorf("ui.Default plain = %v, yet the frame and the row carry escapes = %v: %q", plain, escapes, raw.String())
			}
		})
	}
}

func TestAHandOffThatCannotTakeTheTerminalSaysSo(t *testing.T) {
	d := newDriven(t, 80, 24)
	running(t, d)
	sm := d.m.(sessionModel)
	sm.exec = func(_ tea.ExecCommand, fn tea.ExecCallback) tea.Cmd {
		return func() tea.Msg { return fn(errors.New("release: input reader")) }
	}
	d.m = sm
	reply := make(chan error, 1)
	ran := false
	d.send(handMsg{command: "sbx login", run: func() error { ran = true; return nil }, reply: reply})
	if err := got(t, reply, "hand"); err == nil || !strings.Contains(err.Error(), "lend the terminal") || ran {
		t.Errorf("Hand = %v with run ran = %v, want the release error and no run", err, ran)
	}
}

func TestADialogDrawsOverTheDimmedCard(t *testing.T) {
	d := newDriven(t, 80, 24)
	running(t, d)
	reply := make(chan int, 1)
	d.send(dialogMsg{d: DrawsGlyphsDialog().d, reply: reply})
	view := d.view()
	lines := strings.Split(view, "\n")
	if !strings.HasPrefix(lines[0], "╭── proveo init") {
		t.Errorf("the dialog covered the card's head:\n%s", view)
	}
	if !strings.Contains(view, "what does your terminal draw here?") || !strings.Contains(view, "Prepare sbx on this") {
		t.Errorf("the dialog does not sit over the card:\n%s", view)
	}
	if !strings.Contains(view, "←→ choose · enter press · esc something else · ctrl+c stop") {
		t.Errorf("the dialog's key line does not say ctrl+c stops the run:\n%s", view)
	}
	d.press(typed("x")...)
	none(t, reply, "a key that is no button")
	d.press(teaRight, teaEnter)
	if answer := got(t, reply, "enter on Two icons"); answer != 1 {
		t.Errorf("enter on Two icons = %d, want 1", answer)
	}
	if strings.Contains(d.view(), "what does your terminal draw here?") {
		t.Errorf("the dialog stayed up after its answer:\n%s", d.view())
	}
	if d.stopped() {
		t.Error("answering a dialog stopped the run")
	}
}

func TestCtrlCFromEveryState(t *testing.T) {
	t.Run("before the first page it stops the walk", func(t *testing.T) {
		d := newDriven(t, 80, 24)
		d.press(teaCtrlC)
		if !d.stopped() {
			t.Error("ctrl+c before any page did not cancel the context")
		}
	})
	t.Run("a page at rest asks before it quits", func(t *testing.T) {
		d := newDriven(t, 80, 24)
		reply := d.ask(pageThree())
		d.press(teaCtrlC)
		none(t, reply, "the first ctrl+c")
		if !strings.Contains(d.view(), "Quit init?") {
			t.Fatalf("ctrl+c on a page did not open the quit dialog:\n%s", d.view())
		}
		d.press(teaEnter)
		none(t, reply, "Stay")
		if strings.Contains(d.view(), "Quit init?") {
			t.Fatalf("Stay left the quit dialog up:\n%s", d.view())
		}
		d.press(teaCtrlC, teaCtrlC)
		if out := got(t, reply, "ctrl+c twice"); out != Quit {
			t.Errorf("ctrl+c in the quit dialog = %v, want Quit", out)
		}
		if d.stopped() {
			t.Error("a quit at rest cancelled the run context; the driver ends the walk itself")
		}
	})
	t.Run("between Enter and the plan it stops the run the plan starts", func(t *testing.T) {
		d := newDriven(t, 80, 24)
		reply := d.ask(pageOne(keepAsIs))
		d.press(teaEnter)
		if out := got(t, reply, "enter"); out != Accepted {
			t.Fatalf("enter = %v, want Accepted", out)
		}
		d.press(teaCtrlC)
		if !d.stopped() {
			t.Fatal("ctrl+c after Enter did not cancel the context")
		}
		d.send(planMsg{labels: []string{"Docker login"}})
		if view := d.view(); !strings.Contains(view, "stopping after Docker login …") || strings.Contains(view, "running 1 of 1") {
			t.Errorf("the plan after a ctrl+c does not say it stops:\n%s", view)
		}
	})
	t.Run("a page that answered Back takes no ctrl+c until the next page", func(t *testing.T) {
		d := newDriven(t, 80, 24)
		reply := d.ask(pageTwo())
		d.press(teaEsc)
		if out := got(t, reply, "esc"); out != Back {
			t.Fatalf("esc on page 2 = %v, want Back", out)
		}
		before := d.view()
		d.press(teaCtrlC)
		if d.stopped() {
			t.Error("ctrl+c after Back cancelled the walk with no word on the card")
		}
		if after := d.view(); after != before {
			t.Errorf("ctrl+c after Back changed the card:\nbefore\n%s\nafter\n%s", before, after)
		}
	})
	t.Run("a lone form cancels", func(t *testing.T) {
		d := newDriven(t, 80, 24)
		reply := d.ask(stepForm(1, 1))
		d.press(teaCtrlC)
		if out := got(t, reply, "ctrl+c"); out != Cancelled {
			t.Errorf("ctrl+c on a lone form = %v, want Cancelled", out)
		}
	})
	t.Run("a running page stops after its row", func(t *testing.T) {
		d := newDriven(t, 80, 24)
		f := running(t, d)
		d.press(teaCtrlC)
		if !d.stopped() || !f.Progress.Stopping {
			t.Errorf("ctrl+c while running: stopped %v, stopping %v", d.stopped(), f.Progress.Stopping)
		}
		d.press(teaCtrlC)
		if !strings.Contains(d.view(), "stopping after Docker login …") {
			t.Errorf("a second ctrl+c changed the stopping line:\n%s", d.view())
		}
	})
	t.Run("a dialog during a run presses its ctrl+c button and stops the run", func(t *testing.T) {
		d := newDriven(t, 80, 24)
		running(t, d)
		reply := make(chan int, 1)
		d.send(dialogMsg{d: DrawsGlyphsDialog().d, reply: reply})
		d.press(teaRight, teaCtrlC)
		if answer := got(t, reply, "ctrl+c"); answer != 0 {
			t.Errorf("ctrl+c on the glyph question = %d, want its safe answer 0", answer)
		}
		if !d.stopped() {
			t.Error("ctrl+c on a dialog during a run did not cancel the context")
		}
	})
}

// A sub-page answered over a running page hands the card back to that page's run: the page draws
// its rows as the run left them, and ctrl+c still stops it.
func TestResumeReturnsAPageToItsRun(t *testing.T) {
	d := newDriven(t, 80, 24)
	f := running(t, d)
	sub := d.ask(pageFour())
	d.press(teaEnter)
	if out := got(t, sub, "enter on the sub-page"); out != Accepted {
		t.Fatalf("enter on the sub-page = %v, want Accepted", out)
	}
	d.send(resumeMsg{f: f})
	d.send(statusMsg{label: "Docker login", mark: MarkDone, outcome: "signed in"})
	view := d.view()
	for _, want := range []string{"Prepare sbx on this", "✓ Docker login  signed in"} {
		if !strings.Contains(view, want) {
			t.Errorf("the resumed page lacks %q:\n%s", want, view)
		}
	}
	d.press(teaEnter, teaEsc)
	if d.stopped() {
		t.Fatal("a key other than ctrl+c stopped the resumed run")
	}
	d.press(teaCtrlC)
	if !d.stopped() || !f.Progress.Stopping {
		t.Error("ctrl+c on the resumed run did not stop it")
	}
}

// Edit changes the form up now between two draws; a planned label no row carries still moves the
// key line on.
func TestEditRedrawsRowsAndAPlanLabelWithoutARowCounts(t *testing.T) {
	d := newDriven(t, 80, 24)
	f := pageOne(keepAsIs)
	reply := d.ask(f)
	d.press(teaEnter)
	got(t, reply, "enter")
	d.send(planMsg{labels: []string{"checks", "Docker login"}})
	d.send(statusMsg{label: "checks", mark: MarkRunning, outcome: "sbx diagnose"})
	if view := d.view(); !strings.Contains(view, "running 1 of 2 — checks") {
		t.Errorf("a planned label with no row did not move the key line:\n%s", view)
	}
	d.send(editMsg{edit: func(f *Form) {
		for i := range f.Rows {
			if f.Rows[i].Kind == RowCheck {
				f.Rows[i].Mark = MarkRunning
			}
		}
	}})
	n := 0
	for _, g := range spinFancy {
		n += strings.Count(d.view(), g+" ")
	}
	if n < 4 {
		t.Errorf("the edit did not reach the check rows (%d running):\n%s", n, d.view())
	}
}

// Each planned label binds the first answer row past the row the label before it bound: anthropic
// and cursor wait on the API-key rows, not on the subscription row above them, and a label no row
// carries binds none.
func TestAPlanLabelBindsTheFirstRowPastTheLabelBeforeIt(t *testing.T) {
	d := newDriven(t, 120, 36)
	f := pageThree()
	reply := d.ask(f)
	d.press(teaEnter)
	got(t, reply, "enter")
	d.send(planMsg{labels: []string{"checks", "anthropic", "cursor"}})
	marks := func(label string) []Mark {
		var out []Mark
		for _, r := range f.Rows {
			if r.Label == label {
				out = append(out, r.Mark)
			}
		}
		return out
	}
	if a, c := marks("anthropic"), marks("cursor"); !slices.Equal(a, []Mark{MarkAhead}) || !slices.Equal(c, []Mark{MarkLater, MarkAhead}) {
		t.Errorf("anthropic = %v, cursor = %v; want anthropic and the cursor key planned, the cursor sign-in left", a, c)
	}
	d.send(statusMsg{label: "cursor", mark: MarkRunning, outcome: "storing in sbx"})
	if c := marks("cursor"); !slices.Equal(c, []Mark{MarkLater, MarkRunning}) {
		t.Errorf("cursor = %v, want the key row running", c)
	}
}

// A running page draws every row where its rest drew it: Skip's rows stay inside the walls with
// no button, and the help keeps its slot.
func TestARunningPageWithSkipKeepsItsRowsInPlace(t *testing.T) {
	d := newDriven(t, 80, 24)
	f := pageThree()
	f.Skip = "Skip"
	reply := d.ask(f)
	rest := d.view()
	d.press(teaEnter)
	got(t, reply, "enter")
	d.send(planMsg{labels: []string{"anthropic"}})
	d.send(statusMsg{label: "anthropic", mark: MarkRunning, outcome: "storing in sbx"})
	run := d.view()
	at := func(view, s string) int {
		return slices.IndexFunc(strings.Split(view, "\n"), func(l string) bool { return strings.Contains(l, s) })
	}
	for _, s := range []string{"claudecode", "anthropic", "╰"} {
		if a, b := at(rest, s), at(run, s); a < 0 || a != b {
			t.Errorf("%q drew on row %d at rest and %d running:\nrest\n%s\nrunning\n%s", s, a, b, rest, run)
		}
	}
	if strings.Contains(run, "[ Skip ]") {
		t.Errorf("a running page drew its Skip button:\n%s", run)
	}
}

// Two rows sharing a label take the plan's labels in row order: the second runs while the first
// keeps its answer.
func TestAPlanLabelTwoRowsShareStandsForTheNextOfThem(t *testing.T) {
	d := newDriven(t, 120, 36)
	f := pageThree()
	reply := d.ask(f)
	d.press(teaEnter)
	got(t, reply, "enter")
	d.send(planMsg{labels: []string{"cursor"}})
	var marks []Mark
	for _, r := range f.Rows {
		if r.Label == "cursor" {
			marks = append(marks, r.Mark)
		}
	}
	if !slices.Equal(marks, []Mark{MarkAhead, MarkLater}) {
		t.Errorf("cursor rows = %v, want the first planned and the second left", marks)
	}
	d.send(planMsg{labels: []string{"codex", "cursor"}})
	d.send(statusMsg{label: "cursor", mark: MarkRunning, outcome: "storing"})
	d.send(statusMsg{label: "cursor", mark: MarkDone, outcome: "stored"})
	marks = nil
	for _, r := range f.Rows {
		if r.Label == "cursor" {
			marks = append(marks, r.Mark)
		}
	}
	if !slices.Equal(marks, []Mark{MarkDone, MarkLater}) {
		t.Errorf("cursor rows after the run = %v, want the planned one done", marks)
	}
}
