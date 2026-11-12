// SPEC: _spec/internal/ui/step-lines.puml
package ui

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedTerm is a terminal a test reads while a step's clock still writes to it.
type lockedTerm struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedTerm) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedTerm) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *lockedTerm) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b.Reset()
}

// asDefault makes p the process printer, and the log, for one test.
func asDefault(t *testing.T, p *Printer, log *bytes.Buffer) {
	t.Helper()
	prev, prevLog, clock := Default, logOnly, StepClock
	t.Cleanup(func() { Default, logOnly, StepClock = prev, prevLog, clock })
	Default, StepClock = p, 5*time.Millisecond
	logOnly = nil
	if log != nil {
		logOnly = &Printer{W: log, Plain: true}
	}
}

func await(t *testing.T, term *lockedTerm, want string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if strings.Contains(term.String(), want) {
			return
		}
	}
	t.Fatalf("the terminal never showed %q:\n%q", want, term.String())
}

func TestAStepShowsItsPlaceItsNameAndAClockThenLeavesNoLine(t *testing.T) {
	term, log := &lockedTerm{}, &bytes.Buffer{}
	asDefault(t, &Printer{W: term, Tier: GlyphsSafe, term: term}, log)
	steps := NewSteps(3)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- steps.Do("download sbx", func() error { <-release; return nil }) }()
	await(t, term, "step 1/3 · download sbx · 0s")
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := term.String(); !strings.HasSuffix(got, "\r\x1b[K") {
		t.Errorf("a finished step left its line on the terminal: %q", got)
	}
	for _, want := range []string{"step: download sbx started", "step: download sbx ", " ok"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, log.String())
		}
	}
	if strings.Contains(log.String(), "step 1/3") {
		t.Errorf("a live frame reached the log:\n%s", log.String())
	}
}

func TestAHandedStepPrintsOneStartLineAndNoLiveFrame(t *testing.T) {
	term, log := &lockedTerm{}, &bytes.Buffer{}
	asDefault(t, &Printer{W: term, Tier: GlyphsSafe, term: term}, log)
	steps := NewSteps(2)
	boom := errors.New("sbx login: exit status 1\nsecond line")
	if err := steps.Hand("sign in to Docker", func() error { return boom }); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the step's", err)
	}
	got := term.String()
	if !strings.Contains(got, "step 1/2 · sign in to Docker") || strings.Count(got, "\n") != 1 || strings.Contains(got, "\r") {
		t.Errorf("a handed step must print a start line and no live frame: %q", got)
	}
	if !strings.Contains(log.String(), "failed: sbx login: exit status 1") || strings.Contains(log.String(), "second line") {
		t.Errorf("the log must carry the first line of the failure:\n%s", log.String())
	}
	_ = steps.Do("verify sbx", func() error { return nil })
	if !strings.Contains(term.String(), "step 2/2 · verify sbx") {
		t.Errorf("the next step did not count on from the handed one: %q", term.String())
	}
}

func TestPlainOutputGetsAStartLinePerStep(t *testing.T) {
	var out bytes.Buffer
	asDefault(t, New(&out), nil)
	steps := NewSteps(2)
	_ = steps.Do("download sbx", func() error { return nil })
	_ = steps.Hand("install sbx", func() error { return nil })
	got := out.String()
	if !strings.Contains(got, "step 1/2 · download sbx") || !strings.Contains(got, "step 2/2 · install sbx") {
		t.Errorf("plain output lacks the start lines:\n%s", got)
	}
	if strings.ContainsAny(got, "\r\x1b") {
		t.Errorf("plain output carries terminal control: %q", got)
	}
}

func TestAnAsStepLeavesItsOwnLineAndLogsItsPlace(t *testing.T) {
	term, log := &lockedTerm{}, &bytes.Buffer{}
	asDefault(t, &Printer{W: term, Tier: GlyphsSafe, term: term}, log)
	steps := NewSteps(3)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- steps.DoAs("workload kit", "workload kit", func() error { <-release; return nil }) }()
	await(t, term, "step 1/3 · workload kit · 0s")
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := steps.HandAs("create sandbox", "sbx create", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	steps.StartAs("start claudecode", "claudecode — starting")
	got := term.String()
	for _, want := range []string{"sbx create" + ANSIReset + "\n", "claudecode — starting" + ANSIReset + "\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("the terminal lacks %q: %q", want, got)
		}
	}
	if strings.Contains(got, "step 2/3") || strings.Contains(got, "step 3/3") {
		t.Errorf("a line that stays on the terminal carries its place: %q", got)
	}
	for _, want := range []string{"step 1/3 · workload kit", "step 2/3 · create sandbox", "step: create sandbox started", "step 3/3 · start claudecode"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, log.String())
		}
	}
}

func TestPlainOutputOfAsStepsCarriesNoPlace(t *testing.T) {
	var out bytes.Buffer
	asDefault(t, New(&out), nil)
	steps := NewSteps(3)
	_ = steps.DoAs("workload kit", "workload kit", func() error { return nil })
	_ = steps.HandAs("create sandbox", "sbx create", func() error { return nil })
	steps.StartAs("start claudecode", "claudecode — starting")
	if got := out.String(); got != "workload kit\nsbx create\nclaudecode — starting\n" {
		t.Errorf("plain output = %q, want one unnumbered line per step", got)
	}
}

func TestANilPlanRunsItsStepsAndShowsNothing(t *testing.T) {
	var out bytes.Buffer
	asDefault(t, New(&out), nil)
	var steps *Steps
	ran := 0
	_ = steps.Do("a", func() error { ran++; return nil })
	_ = steps.Hand("b", func() error { ran++; return nil })
	if ran != 2 || out.Len() != 0 {
		t.Errorf("ran %d steps and printed %q; want 2 and nothing", ran, out.String())
	}
}

func TestAPlanThatRunsPastItsCountNeverShowsAStepBeyondTheTotal(t *testing.T) {
	var out bytes.Buffer
	asDefault(t, New(&out), nil)
	steps := NewSteps(1)
	_ = steps.Hand("a", func() error { return nil })
	_ = steps.Hand("b", func() error { return nil })
	if !strings.Contains(out.String(), "step 2/2 · b") {
		t.Errorf("an extra step must widen the total, not read 2/1:\n%s", out.String())
	}
}

func TestASuspendedLiveLineStaysOffTheTerminalUntilItIsResumed(t *testing.T) {
	term := &lockedTerm{}
	p := &Printer{W: term, Tier: GlyphsSafe, term: term}
	p.Live(RoleAsync, "step 2/9 · pushed branches · 1s")
	term.Reset()
	resume := p.SuspendLive()
	if got := term.String(); got != "\r\x1b[K" {
		t.Errorf("suspending must erase the line and draw nothing: %q", got)
	}
	term.Reset()
	p.Live(RoleAsync, "step 2/9 · pushed branches · 2s")
	if got := term.String(); got != "" {
		t.Errorf("a frame reached the terminal while a prompt held it: %q", got)
	}
	inner := p.SuspendLive()
	inner()
	if got := term.String(); got != "" {
		t.Errorf("the line came back while an outer holder still had the terminal: %q", got)
	}
	resume()
	if got := term.String(); !strings.Contains(got, "pushed branches · 2s") {
		t.Errorf("resuming must redraw the latest frame: %q", got)
	}
}

func TestAnOrdinaryLineUnderASuspendedLiveLineNeitherErasesNorRedrawsIt(t *testing.T) {
	term := &lockedTerm{}
	p := &Printer{W: term, Tier: GlyphsSafe, term: term}
	p.Live(RoleAsync, "step 1/2 · create sandbox · 3s")
	resume := p.SuspendLive()
	term.Reset()
	p.Notef("a line while the terminal is lent out")
	if got := term.String(); strings.Contains(got, "\r") || strings.Contains(got, "create sandbox") {
		t.Errorf("a line printed under a suspended live line touched it: %q", got)
	}
	resume()
}

func TestALiveStepDrawsThePendingHeadingBeforeItsLogLines(t *testing.T) {
	term, log := &lockedTerm{}, &bytes.Buffer{}
	asDefault(t, &Printer{W: term, Tier: GlyphsSafe, term: term}, nil)
	TeeTo(log)
	Section(SectionSbx)
	if err := NewSteps(1).DoAs("workload kit", "workload kit", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	got := log.String()
	heading, place := strings.Index(got, " "+SectionSbx+" "), strings.Index(got, "step 1/1 · workload kit")
	if heading < 0 || place < 0 || heading > place {
		t.Errorf("the log must draw the step's heading before its place:\n%s", got)
	}
}

// A quiet step's live frame shows on a terminal; plain output gets no line for it: its caller
// prints what it found under its own heading.
func TestAQuietStepShowsALiveFrameAndLeavesNoPlainLine(t *testing.T) {
	term, log := &lockedTerm{}, &bytes.Buffer{}
	asDefault(t, &Printer{W: term, Tier: GlyphsSafe, term: term}, log)
	steps := NewSteps(2)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- steps.Quiet("verify sbx", func() error { <-release; return nil }) }()
	await(t, term, "step 1/2 · verify sbx · 0s")
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "step 1/2 · verify sbx") {
		t.Errorf("the log lacks the quiet step's place:\n%s", log.String())
	}

	var out bytes.Buffer
	asDefault(t, New(&out), nil)
	if err := NewSteps(1).Quiet("verify sbx", func() error { return nil }); err != nil || out.Len() != 0 {
		t.Errorf("plain output of a quiet step = %q, %v; want nothing", out.String(), err)
	}
}

// A silent step counts toward the plan and logs its place; it prints nothing anywhere.
func TestASilentStepCountsAndPrintsNothing(t *testing.T) {
	term, log := &lockedTerm{}, &bytes.Buffer{}
	asDefault(t, &Printer{W: term, Tier: GlyphsSafe, term: term}, log)
	steps := NewSteps(2)
	ran := false
	if err := steps.Silent("set the sandbox git identity", func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("Silent ran=%v err=%v", ran, err)
	}
	if term.String() != "" {
		t.Errorf("a silent step drew %q", term.String())
	}
	if !strings.Contains(log.String(), "step 1/2 · set the sandbox git identity") {
		t.Errorf("the log lacks the silent step's place:\n%s", log.String())
	}
	_ = steps.HandAs("sign in", "sbx login — signing in to Docker", func() error { return nil })
	if !strings.Contains(log.String(), "step 2/2 · sign in") {
		t.Errorf("the next step did not count on from the silent one:\n%s", log.String())
	}
}
