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
