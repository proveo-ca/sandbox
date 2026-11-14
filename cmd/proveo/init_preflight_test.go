// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml
package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// PAGE 1 CHECKS: one checks section on page 1, what installing needs, then sbx's own rows, each a
// check row with its mark and its fix; the Docker sign-in never draws among them.
func TestTheCheckRowsCarryEveryCheckWithItsMarkAndItsFix(t *testing.T) {
	t.Parallel()
	rows := checkRows(
		[]sbx.Prereq{
			{Name: "Apple silicon", OK: true, Detail: "macOS 14 or later"},
			{Name: "KVM", Blocks: sbx.BlocksInstall, Detail: "/dev/kvm is missing", Fix: "enable virtualization"},
			{Name: "user in kvm group", Detail: "not a member", Fix: "sudo usermod -aG kvm $USER"},
		},
		[]sbx.Check{
			{Name: "daemon", Status: "pass", Detail: "running"},
			{Name: "disk", Status: "warn", Detail: "12 GB free", Hint: "free some space"},
			{Name: "network", Status: "fail", Detail: "no route", Hint: "check the proxy"},
			{Name: "gpu", Status: "skip", Detail: "not checked here"},
			{Name: sbx.AuthenticationCheck, Status: "pass", Message: "authenticated"},
		}, nil)
	want := []struct {
		name string
		mark choiceui.Mark
		fix  string
	}{
		{"Apple silicon", choiceui.MarkDone, ""},
		{"KVM", choiceui.MarkFailed, "enable virtualization"},
		{"user in kvm group", choiceui.MarkNeeds, "sudo usermod -aG kvm $USER"},
		{"daemon", choiceui.MarkDone, ""},
		{"disk", choiceui.MarkNeeds, "free some space"},
		{"network", choiceui.MarkFailed, "check the proxy"},
		{"gpu", choiceui.MarkLater, ""},
	}
	if len(rows) != len(want) {
		t.Fatalf("%d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		if r := rows[i]; r.Kind != choiceui.RowCheck || r.Label != w.name || r.Mark != w.mark || r.Fix != w.fix {
			t.Errorf("row %d = %+v, want %+v", i, r, w)
		}
	}
	if !rows[0].Divider || rows[0].Heading != rowChecks || slices.ContainsFunc(rows[1:], func(r choiceui.Row) bool { return r.Divider }) {
		t.Errorf("want one checks section opened by the first row: %+v", rows)
	}
}

func TestAnSbxRowShowsItsMessageAndKeepsItsDetailAsEvidence(t *testing.T) {
	t.Parallel()
	rows := checkRows(nil, []sbx.Check{{
		Name: "Daemon", Status: "fail", Message: "not reachable",
		Detail: "/run/sbx/sandboxd.sock: connection refused", Hint: "Run: sbx daemon start",
	}}, nil)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	r := rows[0]
	if r.Mark != choiceui.MarkFailed || r.Outcome != "not reachable" ||
		len(r.Evidence) != 1 || r.Evidence[0] != "/run/sbx/sandboxd.sock: connection refused" || r.Fix != "Run: sbx daemon start" {
		t.Errorf("row = %+v, want sbx's message as the outcome, its detail as evidence, its hint as the fix", r)
	}
}

// D19: what a failing check affects is the row's note, apart from its evidence.
func TestAFailingCheckARunDoesNotNeedIsAWarningThatSaysWhatItAffects(t *testing.T) {
	t.Parallel()
	rows := checkRows(nil, []sbx.Check{sshClientConfig}, nil)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	r := rows[0]
	if r.Mark != choiceui.MarkNeeds || len(r.Evidence) != 1 || r.Evidence[0] != "/home/op/.ssh/config:5: Host" ||
		!strings.Contains(r.Note, "a run does not need this check; it affects IDE attach and Hermes Desktop") {
		t.Errorf("row = %+v, want a warning whose note says what the check affects", r)
	}
}

// D7: a fix that points at "the link above" takes the link into itself, so it stands alone when the
// ladder drops the evidence.
func TestAFixStandsAloneWithoutTheLinkAbove(t *testing.T) {
	t.Parallel()
	rows := checkRows(nil, []sbx.Check{{Name: "Binary version", Status: "warn", Message: "update available: v9.9.9",
		Detail: "https://example.invalid/sbx/v9.9.9", Hint: "Download the latest release from the link above"}}, nil)
	if r := rows[0]; r.Fix != "Download the latest release: https://example.invalid/sbx/v9.9.9" || len(r.Evidence) != 0 {
		t.Errorf("row = %+v, want the link inside the fix and no evidence line left", r)
	}
	if fix, ev := standAlone("Run: sbx daemon start", []string{"https://x.invalid"}); fix != "Run: sbx daemon start" || len(ev) != 1 {
		t.Errorf("a fix that points nowhere changed: %q %q", fix, ev)
	}
}

func TestOnlyAFailingCheckARunNeedsFailsInit(t *testing.T) {
	daemon := sbx.Check{Name: "Daemon", Status: "fail", Message: "not reachable", Hint: "Run: sbx daemon start"}
	pass := sbx.Check{Name: "Socket", Status: "pass", Message: "responsive"}
	for _, c := range []struct {
		name     string
		checks   []sbx.Check
		blockers []string
		says     []string
	}{
		{"a check a run does not need", []sbx.Check{pass, sshClientConfig}, nil,
			[]string{"⚠ SSH client config — an unmanaged sandbox Host stanza", "/home/op/.ssh/config:5: Host", "Remove or rename",
				"a run does not need this check; it affects IDE attach"}},
		{"a check a run needs", []sbx.Check{pass, daemon, sshClientConfig}, []string{"Daemon"},
			[]string{"× Daemon — not reachable", "Run: sbx daemon start"}},
		{"a check proveo does not know", []sbx.Check{{Name: "Something new", Status: "fail", Message: "broken"}}, []string{"Something new"},
			[]string{"× Something new — broken", "`proveo init --force` reinstalls the pinned " + sbx.Release}},
		{"a signed-out Authentication row", []sbx.Check{pass, {Name: sbx.AuthenticationCheck, Status: "fail", Message: "not logged in"}}, nil,
			[]string{"⚠ Docker login — not signed in", loginFix}},
		{"an Authentication row that warns", []sbx.Check{pass, {Name: sbx.AuthenticationCheck, Status: "warn", Message: "not logged in"}}, nil,
			[]string{"⚠ Docker login — not signed in", loginFix}},
		{"every check passed", []sbx.Check{pass}, nil, nil},
	} {
		items, blockers := checkItems(c.checks)
		if got := sbx.CheckNames(blockers); !slices.Equal(got, c.blockers) {
			t.Errorf("%s: init stops on %v, want %v", c.name, got, c.blockers)
		}
		var said []string
		for _, it := range items {
			mark := "⚠ "
			if it.Fail {
				mark = "× "
			}
			said = append(append(said, mark+it.Text), it.Detail...)
		}
		out := strings.Join(said, "\n")
		for _, want := range c.says {
			if !strings.Contains(out, want) {
				t.Errorf("%s: the items lack %q:\n%s", c.name, want, out)
			}
		}
		if strings.Contains(out, "checks passed") || strings.Contains(out, "host checks failing") || strings.Contains(out, "does not manage the host") {
			t.Errorf("%s: a count or the --force sentence is back on screen:\n%s", c.name, out)
		}
		if c.says == nil && len(items) != 0 {
			t.Errorf("%s: a passing host printed items: %+v", c.name, items)
		}
	}
}

var sshClientConfig = sbx.Check{
	Name: "SSH client config", Status: "fail",
	Message: "an unmanaged sandbox Host stanza can shadow the managed configuration",
	Detail:  "/home/op/.ssh/config:5: Host",
	Hint:    "Remove or rename the reported Host *.sbx stanza in /home/op/.ssh/config, then run `sbx setup ssh`",
}

func TestAHostWithoutSbxShowsOnlyWhatInstallingNeeds(t *testing.T) {
	t.Parallel()
	rows := checkRows([]sbx.Prereq{{Name: "Apple silicon", OK: true}}, nil, nil)
	if len(rows) != 1 || rows[0].Label != "Apple silicon" {
		t.Errorf("rows = %+v, want the one install prerequisite", rows)
	}
}

func TestAnSbxThatDoesNotAnswerIsARowAndNotAnError(t *testing.T) {
	t.Parallel()
	rows := checkRows(nil, nil, errors.New("exec: sbx: timed out\nmore"))
	if len(rows) != 1 || rows[0].Mark != choiceui.MarkNeeds || !strings.Contains(rows[0].Outcome, "timed out") ||
		strings.Contains(rows[0].Outcome, "more") || !strings.Contains(rows[0].Fix, "proveo init --force") {
		t.Errorf("rows = %+v", rows)
	}
}

func TestTheResultLeavesOutAnActionThatRanOnAnEarlierVisit(t *testing.T) {
	t.Parallel()
	rec := &initRecord{}
	first := initDecisions{Login: true, Wizard: true}
	rec.leaveUnchosen(first)
	for _, label := range []string{rowLogin, rowWizard, "", labelVerify, rowLogin} {
		rec.did(label)
	}
	rec.leaveUnchosen(initDecisions{}) // the revisit: every one-shot action opens on later
	rec.leave(labelCredentials)
	later, notRun := rec.lines()
	if want := []string{rowGh, rowGit, rowPrompt, labelCredentials}; !slices.Equal(later, want) {
		t.Errorf("later = %q\n want  %q", later, want)
	}
	if len(notRun) != 0 {
		t.Errorf("not run = %q on an init that ran to its end", notRun)
	}
	rec.did(labelCredentials)
	if later, _ = rec.lines(); slices.Contains(later, labelCredentials) {
		t.Errorf("an action that ran after being left is still listed as later: %q", later)
	}
}

func TestSignInLeftForLaterUsesTheSetupPagesRowNames(t *testing.T) {
	t.Parallel()
	rec := &initRecord{}
	rec.leaveUnchosen(initDecisions{Git: true, Prompt: true, Gh: true})
	if later, _ := rec.lines(); !slices.Equal(later, []string{rowLogin, rowWizard}) {
		t.Errorf("later = %q, want the setup page's own rows", later)
	}
}

// UNREACHED: what a failure kept from running stacks under its own "not run" key; "later" keeps
// only what was answered later.
func TestAFailureNamesWhatItKeptFromRunningApartFromLater(t *testing.T) {
	t.Parallel()
	rec := &initRecord{}
	rec.leaveUnchosen(initDecisions{Login: true, Wizard: true, Gh: true})
	rec.did(rowInstall)
	rec.unreached(rowWizard, rowGh, labelVerify, rowGh)
	rec.unreached(labelCredentials, labelOperator)
	later, notRun := rec.lines()
	if !slices.Equal(later, []string{rowGit, rowPrompt}) {
		t.Errorf("later = %q", later)
	}
	if want := []string{rowWizard, rowGh, labelVerify, labelCredentials, labelOperator}; !slices.Equal(notRun, want) {
		t.Errorf("not run = %q, want %q", notRun, want)
	}
	var keys []string
	for _, f := range rec.facts() {
		keys = append(keys, f.Key)
	}
	if want := []string{"later", "", "not run", "", "", "", ""}; !slices.Equal(keys, want) {
		t.Errorf("fact keys = %q, want each list stacked under its key", keys)
	}
}

// A page answered "later" that a failure then kept init from reaching again stays "later" alone.
func TestARowAnsweredLaterIsNeverAlsoNotRun(t *testing.T) {
	t.Parallel()
	rec := &initRecord{}
	rec.leave(labelCredentials)
	rec.unreached(labelCredentials, labelOperator)
	later, notRun := rec.lines()
	if !slices.Equal(later, []string{labelCredentials}) || !slices.Equal(notRun, []string{labelOperator}) {
		t.Errorf("later = %q, not run = %q; want credentials under later only", later, notRun)
	}
}

// A row answered "later", then chosen "now" on a revisit that fails before it runs, is "not run"
// and no longer "later"; the row that failed is neither.
func TestARowChosenNowOnARevisitLeavesLater(t *testing.T) {
	t.Parallel()
	rec := &initRecord{}
	rec.leaveUnchosen(initDecisions{Wizard: true, Gh: true, Prompt: true})
	for _, label := range []string{rowWizard, rowGh, rowPrompt, labelVerify} {
		rec.did(label)
	}
	rec.leaveUnchosen(initDecisions{Login: true, Git: true})
	rec.unreached(rowGit, labelVerify)
	rec.unreached(labelCredentials, labelOperator)
	later, notRun := rec.lines()
	if len(later) != 0 {
		t.Errorf("later = %q; the revisit chose them now", later)
	}
	if want := []string{rowGit, labelCredentials, labelOperator}; !slices.Equal(notRun, want) {
		t.Errorf("not run = %q, want %q", notRun, want)
	}
}

// A page that fails names every page after it as never run, and the steps it would run; a quit and a
// blocked host do not.
func TestAFailedPageNamesThePagesAfterIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  error
		want []string
	}{
		{errors.New("boom"), []string{rowGit, labelCredentials, labelOperator}},
		{fmt.Errorf("%w at page 1/3", errInitLeft), nil},
		{blockedErr{}, nil},
	} {
		rec := &initRecord{}
		steps := rec.pages([]initPage{
			{run: func(choiceui.Step) (stepResult, error) { return stepNext, tc.err }},
			{rows: []string{rowGit}, label: labelCredentials, run: func(choiceui.Step) (stepResult, error) { return stepNext, nil }},
			{label: labelOperator, run: func(choiceui.Step) (stepResult, error) { return stepNext, nil }},
		})
		_ = runInitSteps(false, steps)
		if _, notRun := rec.lines(); !slices.Equal(notRun, tc.want) {
			t.Errorf("%v: not run = %q, want %q", tc.err, notRun, tc.want)
		}
	}
	rec := &initRecord{}
	_ = runInitSteps(true, rec.pages([]initPage{{run: func(choiceui.Step) (stepResult, error) { return stepQuit, nil }}, {label: labelOperator}}))
	if _, notRun := rec.lines(); len(notRun) != 0 {
		t.Errorf("a quit named %q as never run", notRun)
	}
}

// A step the host left nothing to do for is never "later": Docker login on a signed-in host, sbx
// setup with nothing to import.
func TestAMootStepIsNeverLeftForLater(t *testing.T) {
	t.Parallel()
	rec := &initRecord{}
	rec.leaveUnchosen(initDecisions{}, rowLogin)
	if later, _ := rec.lines(); slices.Contains(later, rowLogin) || !slices.Contains(later, rowWizard) {
		t.Errorf("later = %q, want Docker login out and sbx setup in", later)
	}
	rec.moot(rowWizard)
	if later, _ := rec.lines(); slices.Contains(later, rowWizard) {
		t.Errorf("later = %q after the probe found nothing", later)
	}

	rep := &initReport{}
	if got := rep.mootRows(); len(got) != 0 {
		t.Errorf("no diagnose yet, moot = %q", got)
	}
	rep.heard([]sbx.Check{{Name: sbx.AuthenticationCheck, Status: "pass", Message: "authenticated"}}, nil)
	if got := rep.mootRows(); !slices.Equal(got, []string{rowLogin}) {
		t.Errorf("signed in, moot = %q", got)
	}
	rep.heard([]sbx.Check{{Name: sbx.AuthenticationCheck, Status: "fail", Message: "not signed in"}}, nil)
	if got := rep.mootRows(); len(got) != 0 {
		t.Errorf("signed out, moot = %q", got)
	}

	rec = &initRecord{}
	rec.leaveUnchosen(initDecisions{})
	rep = &initReport{asked: askedByFlag, probe: func() (sbx.SetupOffer, error) { return sbx.SetupOffer{Known: true}, nil }}
	rep.heard([]sbx.Check{{Name: sbx.AuthenticationCheck, Status: "pass"}}, nil)
	rec.moot(rep.mootRows()...)
	if turn := rep.planSetup(rec, nil); turn.runs || turn.item == nil || turn.item.Text != "sbx setup — nothing to import" {
		t.Errorf("turn = %+v", turn)
	}
	if later, _ := rec.lines(); slices.Contains(later, rowLogin) || slices.Contains(later, rowWizard) {
		t.Errorf("--yes on a signed-in host with nothing to import left %q for later", later)
	}
}
