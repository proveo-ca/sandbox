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

func TestThePreflightRowsCarryEveryCheckWithItsStateAndItsFix(t *testing.T) {
	t.Parallel()
	rows := preflightRows(
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
		}, nil)
	want := []struct {
		group, name string
		state       choiceui.CheckState
		hint        string
	}{
		{checksToInstall, "Apple silicon", choiceui.CheckOK, ""},
		{checksToInstall, "KVM", choiceui.CheckFail, "enable virtualization"},
		{checksToInstall, "user in kvm group", choiceui.CheckWarn, "sudo usermod -aG kvm $USER"},
		{checksFromSbx, "daemon", choiceui.CheckOK, ""},
		{checksFromSbx, "disk", choiceui.CheckWarn, "free some space"},
		{checksFromSbx, "network", choiceui.CheckFail, "check the proxy"},
		{checksFromSbx, "gpu", choiceui.CheckSkip, ""},
	}
	if len(rows) != len(want) {
		t.Fatalf("%d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		if r := rows[i]; r.Group != w.group || r.Name != w.name || r.State != w.state || r.Hint != w.hint {
			t.Errorf("row %d = %+v, want %+v", i, r, w)
		}
	}
}

func TestAnSbxRowShowsItsMessageAndKeepsItsDetailAsEvidence(t *testing.T) {
	t.Parallel()
	rows := preflightRows(nil, []sbx.Check{{
		Name: "Daemon", Status: "fail", Message: "not reachable",
		Detail: "/run/sbx/sandboxd.sock: connection refused", Hint: "Run: sbx daemon start",
	}}, nil)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	r := rows[0]
	if r.State != choiceui.CheckFail || r.Detail != "not reachable" ||
		len(r.Evidence) != 1 || r.Evidence[0] != "/run/sbx/sandboxd.sock: connection refused" || r.Hint != "Run: sbx daemon start" {
		t.Errorf("row = %+v, want sbx's message as the detail, its detail as evidence, its hint as the hint", r)
	}
}

func TestAFailingCheckARunDoesNotNeedIsAWarningThatSaysWhatItAffects(t *testing.T) {
	t.Parallel()
	rows := preflightRows(nil, []sbx.Check{sshClientConfig}, nil)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	r := rows[0]
	if r.State != choiceui.CheckWarn || len(r.Evidence) != 2 || r.Evidence[0] != "/home/op/.ssh/config:5: Host" ||
		!strings.Contains(r.Evidence[1], "a run does not need this check; it affects IDE attach and Hermes Desktop") {
		t.Errorf("row = %+v, want a warning whose evidence ends on what the check affects", r)
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
	rows := preflightRows([]sbx.Prereq{{Name: "Apple silicon", OK: true}}, nil, nil)
	if len(rows) != 1 || rows[0].Group != checksToInstall {
		t.Errorf("rows = %+v, want the one install prerequisite", rows)
	}
}

func TestAnSbxThatDoesNotAnswerIsARowAndNotAnError(t *testing.T) {
	t.Parallel()
	rows := preflightRows(nil, nil, errors.New("exec: sbx: timed out\nmore"))
	if len(rows) != 1 || rows[0].State != choiceui.CheckWarn || !strings.Contains(rows[0].Detail, "timed out") ||
		strings.Contains(rows[0].Detail, "more") || !strings.Contains(rows[0].Hint, "proveo init --force") {
		t.Errorf("rows = %+v", rows)
	}
}

func TestTheResultLeavesOutAnActionThatRanOnAnEarlierVisit(t *testing.T) {
	t.Parallel()
	rec := &initRecord{}
	first := initDecisions{Login: true, Wizard: true}
	rec.leaveUnchosen(first)
	for _, label := range []string{rowSignIn, rowWizard, "", labelVerify, rowSignIn} {
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
	if later, _ := rec.lines(); !slices.Equal(later, []string{rowSignIn, rowWizard}) {
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

// A page that fails names every page after it as never run; a quit, a cancel and a blocked host do not.
func TestAFailedPageNamesThePagesAfterIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  error
		want []string
	}{
		{errors.New("boom"), []string{labelCredentials, labelOperator}},
		{fmt.Errorf("%w at the setup page", errInitCancel), nil},
		{blockedErr{}, nil},
	} {
		rec := &initRecord{}
		steps := rec.pages([]initPage{
			{run: func(choiceui.Step) (stepResult, error) { return stepNext, tc.err }},
			{label: labelCredentials, run: func(choiceui.Step) (stepResult, error) { return stepNext, nil }},
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
