// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml
package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// recorder is a screen that logs its page and answers from a script, then stepNext.
func recorder(seen *[]string, name string, script ...stepResult) initStep {
	return func(at choiceui.Step) (stepResult, error) {
		*seen = append(*seen, fmt.Sprintf("%s@%d/%d", name, at.At, at.Of))
		if len(script) == 0 {
			return stepNext, nil
		}
		r := script[0]
		script = script[1:]
		return r, nil
	}
}

func TestInitStepsWalkBackOneScreenAtATime(t *testing.T) {
	var seen []string
	steps := []initStep{
		recorder(&seen, "setup"),
		recorder(&seen, "creds", stepBack),
		recorder(&seen, "op", stepBack),
	}
	if err := runInitSteps(true, steps); err != nil {
		t.Fatal(err)
	}
	want := "setup@1/3 creds@2/3 setup@1/3 creds@2/3 op@3/3 creds@2/3 op@3/3"
	if got := strings.Join(seen, " "); got != want {
		t.Errorf("screens visited %q, want %q", got, want)
	}
}

// A screen with nothing to ask is passed over on the way back, never stopped at.
func TestAStepBackPassesOverAScreenThatAsksNothing(t *testing.T) {
	var seen []string
	steps := []initStep{
		recorder(&seen, "setup"),
		recorder(&seen, "creds", stepNone, stepNone),
		recorder(&seen, "op", stepBack),
	}
	if err := runInitSteps(true, steps); err != nil {
		t.Fatal(err)
	}
	want := "setup@1/3 creds@2/3 op@3/3 creds@2/3 setup@1/3 creds@2/3 op@3/3"
	if got := strings.Join(seen, " "); got != want {
		t.Errorf("screens visited %q, want %q", got, want)
	}
}

// --yes and a missing terminal number no screen and honour no step back.
func TestAnUnpromptedInitNeverStepsBack(t *testing.T) {
	var seen []string
	steps := []initStep{recorder(&seen, "setup"), recorder(&seen, "creds", stepBack), recorder(&seen, "op")}
	if err := runInitSteps(false, steps); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(seen, " "), "setup@0/0 creds@0/0 op@0/0"; got != want {
		t.Errorf("screens visited %q, want %q", got, want)
	}
}

func TestInitStepsIgnoreABackFromTheFirstScreenAndStopOnAnError(t *testing.T) {
	calls := 0
	first := func(choiceui.Step) (stepResult, error) {
		calls++
		if calls == 1 {
			return stepBack, nil
		}
		return stepNext, nil
	}
	boom := errors.New("cancelled")
	second := func(choiceui.Step) (stepResult, error) { return stepNext, boom }
	if err := runInitSteps(true, []initStep{first, second}); !errors.Is(err, boom) {
		t.Errorf("runInitSteps = %v, want the second screen's error", err)
	}
	if calls != 1 {
		t.Errorf("the first screen ran %d times; a back from it has nowhere to go", calls)
	}
}

// A revisit must not re-run what already ran: every one-shot action opens on later, and each row whose
// step ran locks to its outcome.
func TestARevisitOpensOnWhatIsLeft(t *testing.T) {
	plan := sbx.Plan{Prefix: "/home/u/.docker/sbx"}
	def := defaultDecisions(plan, "0.50.0", initOptions{}, true)
	prev := &initDecisions{Install: installTarball, Prefix: "/usr/local", AddPath: true,
		Login: true, Wizard: true, Gh: true, Git: true, Baseline: sbx.BaselineDenyAll}
	got := carried(def, prev)
	if got.Install != installSkip || got.Prefix != plan.Prefix {
		t.Errorf("install/prefix = %q/%q, want what the host has now: %q/%q", got.Install, got.Prefix, installSkip, plan.Prefix)
	}
	if got.Login || got.Gh || got.Git || got.Baseline != "" {
		t.Errorf("a revisit would re-run sbx login, gh, git or the baseline: %+v", got)
	}
	if !got.Wizard {
		t.Error("a revisit dropped \"after init\": sbx setup has not run yet, it runs after the result")
	}
	if def.AddPath || !got.AddPath {
		t.Errorf("the PATH answer was not kept (default %v, revisit %v)", def.AddPath, got.AddPath)
	}
	if carried(def, nil) != def {
		t.Error("a first visit must open on the defaults")
	}

	seen := map[string]rowState{
		rowLogin: {mark: choiceui.MarkDone, outcome: "signed in"},
		rowGit:   {mark: choiceui.MarkNeeds, outcome: "not set · both fields needed"},
		rowGh:    {mark: choiceui.MarkLater, outcome: "later"},
	}
	in := sbxPageIn{host: sbx.Host{OS: "linux", Arch: "amd64"}, plan: plan, installed: "0.50.0", diagnosed: true, auth: sbx.AuthOut}
	none := func(initDecisions) int { return 0 }
	rows := append(sbxForm(in, initStepAt(1), got, initOptions{}, seen, none).Rows,
		gitForm(gitPageIn{gitOK: true}, initStepAt(2), got, initOptions{}, nil, seen, none).Rows...)
	for _, c := range []struct {
		label  string
		locked bool
		reads  string
	}{
		{rowLogin, true, "signed in"},
		{rowGit, true, "not set · both fields needed"},
		{rowGh, false, ""},
		{rowInstall, false, ""},
	} {
		at := slices.IndexFunc(rows, func(r choiceui.Row) bool { return r.Label == c.label })
		if at < 0 {
			t.Fatalf("no %s row on the revisit", c.label)
		}
		r := rows[at]
		if locked := r.Kind == choiceui.RowStatus; locked != c.locked || locked && r.Outcome != c.reads {
			t.Errorf("revisit %s = %+v; a row that ran locks to its outcome, one left – stays an answer", c.label, r)
		}
	}
	if slices.ContainsFunc(rows, func(r choiceui.Row) bool {
		return r.Label == rowName && r.Kind == choiceui.RowAnswer && r.When != nil && r.When.Row == rowGit
	}) {
		if f := gitForm(gitPageIn{gitOK: true}, initStepAt(2), got, initOptions{}, nil, seen, none); f.Selection(rowGit) != "" {
			t.Error("a locked git identity still opens its fields")
		}
	}
}

// After a distro-package install, setup acts without the tarball's prefix whether it reinstalls or keeps.
func TestTheAppliedPlanFollowsWhereSbxCameFrom(t *testing.T) {
	plan := sbx.Plan{Prefix: "/home/u/.docker/sbx", Bin: "/home/u/.docker/sbx/bin/sbx"}
	if got := appliedPlan(plan, true); got.Bin != "" || got.Prefix != "" {
		t.Errorf("a package install acted on %q / %q; the package manager owns placement", got.Bin, got.Prefix)
	}
	if got := appliedPlan(plan, false); got.Bin != plan.Bin || got.Prefix != plan.Prefix {
		t.Errorf("a tarball install acted on %q / %q, want the plan's own", got.Bin, got.Prefix)
	}
	if plan.Bin == "" {
		t.Error("appliedPlan changed the plan a revisit still needs")
	}
}

func TestAQuitEndsInitThereAndSaysWhichPage(t *testing.T) {
	t.Parallel()
	var seen []string
	steps := []initStep{
		recorder(&seen, "setup"),
		recorder(&seen, "creds", stepQuit),
		recorder(&seen, "op"),
	}
	err := runInitSteps(true, steps)
	if err == nil || !strings.Contains(err.Error(), "quit at page 2/3") {
		t.Errorf("runInitSteps = %v, want a quit at page 2/3", err)
	}
	if got := strings.Join(seen, " "); strings.Contains(got, "op") {
		t.Errorf("a page ran after the quit: %s", got)
	}
}

// fakeSession is a choiceui.Session in memory: it answers each Ask from a script, records every
// call in order, and runs a hand-off's outcome without a child.
type fakeSession struct {
	asked   []*choiceui.Form
	script  []choiceui.Outcome // the answer to each Ask in turn; Accepted past its end
	edit    func(*choiceui.Form, int)
	events  []string
	up      *choiceui.Form
	ctx     context.Context
	cancel  context.CancelFunc
	closed  int
	handErr error
}

func newFakeSession(script ...choiceui.Outcome) *fakeSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &fakeSession{script: script, ctx: ctx, cancel: cancel}
}

func (s *fakeSession) Ask(f *choiceui.Form) (choiceui.Outcome, error) {
	s.asked = append(s.asked, f)
	s.up = f
	s.events = append(s.events, fmt.Sprintf("ask %s @%d/%d", f.Title, f.Step.At, f.Step.Of))
	if s.edit != nil {
		s.edit(f, len(s.asked))
	}
	if len(s.script) == 0 {
		return choiceui.Accepted, nil
	}
	out := s.script[0]
	s.script = s.script[1:]
	return out, nil
}

func (s *fakeSession) Resume(f *choiceui.Form) {
	s.up = f
	s.events = append(s.events, "resume "+f.Title)
}

func (s *fakeSession) Plan(labels []string) {
	s.events = append(s.events, "plan "+strings.Join(labels, ", "))
}

func (s *fakeSession) Status(label string, m choiceui.Mark, outcome string) {
	s.events = append(s.events, fmt.Sprintf("status %s %s %s", label, m.Glyph(choiceui.GlyphsNerd, 0), outcome))
}

func (s *fakeSession) Edit(edit func(*choiceui.Form)) {
	s.events = append(s.events, "edit")
	if s.up != nil {
		edit(s.up)
	}
}

func (s *fakeSession) Hand(command string, parts []string, _ func() error, outcome func(error) (choiceui.Mark, string)) error {
	s.events = append(s.events, "hand "+command+" ── "+strings.Join(parts, " · "))
	if outcome != nil {
		m, text := outcome(s.handErr)
		s.events = append(s.events, fmt.Sprintf("handed %s %s", m.Glyph(choiceui.GlyphsNerd, 0), text))
	}
	return s.handErr
}

func (s *fakeSession) Dialog(choiceui.Dialog) (int, error) {
	s.events = append(s.events, "dialog")
	return 0, nil
}

func (s *fakeSession) Context() context.Context { return s.ctx }

func (s *fakeSession) Close() error {
	s.closed++
	s.cancel()
	return nil
}

// with returns the events that start with prefix.
func (s *fakeSession) with(prefix string) []string {
	var out []string
	for _, e := range s.events {
		if strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

// fakeWalk is a prompted init on the stub sbx whose card is s: every flag that would reach past the
// stub is set.
func fakeWalk(t *testing.T, s *fakeSession, o initOptions) *initWalk {
	t.Helper()
	isolatedInitHost(t)
	prev := openCard
	t.Cleanup(func() { openCard = prev })
	openCard = func() (cardSession, error) { return s, nil }
	host := sbx.Host{OS: "darwin", Arch: "arm64"}
	plan := sbx.Plan{Prefix: filepath.Join(t.TempDir(), "prefix")}
	rep := newInitReport(o, host, []sbx.Prereq{{Name: "Apple silicon", OK: true}}, plan)
	rep.probe = func() (sbx.SetupOffer, error) { return sbx.SetupOffer{Known: true}, nil }
	return &initWalk{o: o, host: host, plan: plan, download: t.TempDir(), installed: "0.50.0",
		prereqs: rep.prereqs, rep: rep, rec: &initRecord{}, prompted: true, typed: map[string]string{}}
}

// ONE SESSION PER WALK: a prompted init opens one card and asks its four pages on it in order,
// each with chip page n/4 and the question that names it; the card closes once, before the result.
func TestInitWalksFourPagesInOneSession(t *testing.T) {
	out := captureUI(t)
	s := newFakeSession(choiceui.Accepted, choiceui.Accepted, choiceui.Skipped, choiceui.Accepted)
	w := fakeWalk(t, s, initOptions{skipLogin: true, skipSetup: true, skipGh: true, skipGit: true, skipPrompt: true})
	if err := w.onCard(); err != nil {
		t.Fatalf("the walk = %v\n%s", err, out)
	}
	want := []string{"Prepare sbx on this darwin/arm64 host", "Set up git and the shell", "Store agent credentials", "Name the operator"}
	if len(s.asked) != len(want) {
		t.Fatalf("asked %d pages, want %d: %q", len(s.asked), len(want), s.with("ask"))
	}
	for i, f := range s.asked {
		if f.Title != want[i] || f.Step.At != i+1 || f.Step.Of != initPages || !f.Checklist || f.Border != initBorder {
			t.Errorf("page %d = %q @%d/%d checklist %v, want %q @%d/4 on the card", i+1, f.Title, f.Step.At, f.Step.Of, f.Checklist, want[i], i+1)
		}
		if f.Step.Leave.Title != "Quit init?" || !strings.Contains(f.Step.Leave.Body, fmt.Sprintf("Answers on page %d/4 are not saved.", i+1)) {
			t.Errorf("page %d's quit question = %+v", i+1, f.Step.Leave)
		}
		if done := countKind(f, choiceui.RowDone); done != i {
			t.Errorf("page %d draws %d done rows, want %d", i+1, done, i)
		}
	}
	if s.closed != 1 {
		t.Errorf("the card closed %d times, want once", s.closed)
	}
	if got := out.String(); !strings.Contains(got, "------ results ------") {
		t.Errorf("the result did not print after the card:\n%s", got)
	}
}

// countKind is how many of f's rows are of kind k.
func countKind(f *choiceui.Form, k choiceui.RowKind) int {
	n := 0
	for _, r := range f.Rows {
		if r.Kind == k {
			n++
		}
	}
	return n
}

// A confirmed quit on any page, Esc on page 1 included, ends init as "quit at page n/4", and no page
// after it is asked.
func TestAQuitAtAnyPageSaysQuitAtPageNOf4(t *testing.T) {
	for n := 1; n <= initPages; n++ {
		script := append(slices.Repeat([]choiceui.Outcome{choiceui.Accepted}, n-1), choiceui.Quit)
		if n == 4 {
			script[2] = choiceui.Skipped
		}
		s := newFakeSession(script...)
		out := captureUI(t)
		w := fakeWalk(t, s, initOptions{skipLogin: true, skipSetup: true, skipGh: true, skipGit: true, skipPrompt: true})
		err := w.onCard()
		if code, ok := exitCode(err); !ok || code != 1 {
			t.Errorf("page %d: a quit = %v, want exit 1", n, err)
		}
		if want := fmt.Sprintf("warn: init — quit at page %d/4", n); !strings.Contains(out.String(), want) {
			t.Errorf("page %d: the ending lacks %q:\n%s", n, want, out.String())
		}
		if len(s.asked) != n || s.closed != 1 {
			t.Errorf("page %d: asked %d pages and closed %d times", n, len(s.asked), s.closed)
		}
	}
	page1 := fakeWalk(t, newFakeSession(), initOptions{})
	in := sbxPageIn{host: page1.host, plan: page1.plan, installed: "0.50.0", diagnosed: true, auth: sbx.AuthIn}
	f := sbxForm(in, initStepAt(1), defaultDecisions(page1.plan, "0.50.0", initOptions{}, true), initOptions{}, nil, func(initDecisions) int { return 0 })
	if !f.Checklist || f.Step.At != 1 || f.Step.Of != initPages {
		t.Errorf("page 1 is not a checklist page of the walk, so Esc there would not ask: %+v", f.Step)
	}
}
