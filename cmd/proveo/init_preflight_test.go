// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml
package main

import (
	"errors"
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
			[]string{"sbx reports 1 of 2 host checks failing", "SSH client config — an unmanaged sandbox Host stanza",
				"/home/op/.ssh/config:5: Host", "Remove or rename", "a run does not need this check; it affects IDE attach"}},
		{"a check a run needs", []sbx.Check{pass, daemon, sshClientConfig}, []string{"Daemon"},
			[]string{"sbx reports 2 of 3 host checks failing", "Daemon — not reachable", "Run: sbx daemon start"}},
		{"a check proveo does not know", []sbx.Check{{Name: "Something new", Status: "fail", Message: "broken"}}, []string{"Something new"},
			[]string{"Something new — broken"}},
		{"every check passed", []sbx.Check{pass}, nil, []string{"sbx reports this host ready — 1 checks passed"}},
	} {
		out := captureUI(t)
		got := sbx.CheckNames(reportChecks(c.checks))
		if !slices.Equal(got, c.blockers) {
			t.Errorf("%s: init stops on %v, want %v", c.name, got, c.blockers)
		}
		for _, want := range c.says {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%s: the report lacks %q:\n%s", c.name, want, out)
			}
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

func TestTheResultCountsAnActionThatRanOnAnEarlierVisitAsDone(t *testing.T) {
	t.Parallel()
	rec := &initRecord{}
	first := initDecisions{Login: true, Wizard: true}
	rec.leaveUnchosen(first)
	for _, name := range []string{actSignIn, actWizard, "", "verify sbx (sbx diagnose)", actSignIn} {
		rec.did(name)
	}
	rec.leaveUnchosen(initDecisions{}) // the revisit: every one-shot action opens on later
	rec.leave(actCreds, "proveo init")
	ran, later, next := rec.lines()
	if !slices.Equal(ran, []string{actSignIn, actWizard, "verify sbx (sbx diagnose)"}) {
		t.Errorf("ran = %q; each action once, in the order it ran", ran)
	}
	wantLater := []string{
		actGit + " — `proveo init`", actPrompt + " — `proveo init`", actGitHub + " — `proveo init`", actCreds + " — `proveo init`",
	}
	if !slices.Equal(later, wantLater) {
		t.Errorf("later = %q\n want  %q", later, wantLater)
	}
	if next != "proveo run" {
		t.Errorf("next = %q", next)
	}
	rec.did(actCreds)
	if _, later, _ = rec.lines(); slices.Contains(later, actCreds+" — `proveo init`") {
		t.Errorf("an action that ran after being left is still listed as later: %q", later)
	}
}

func TestSignInLeftForLaterNamesSbxsOwnCommand(t *testing.T) {
	t.Parallel()
	rec := &initRecord{}
	rec.leaveUnchosen(initDecisions{Git: true, Prompt: true, Gh: true})
	_, later, _ := rec.lines()
	want := []string{actSignIn + " — `" + sbx.Binary + " login`", actWizard + " — `" + sbx.Binary + " setup`"}
	if !slices.Equal(later, want) {
		t.Errorf("later = %q, want %q", later, want)
	}
}
