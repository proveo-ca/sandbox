// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml
package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestInitStepsWalkBackOneScreenAtATime(t *testing.T) {
	var seen []string
	backs := map[int]int{2: 1, 3: 1} // screen → steps back it answers before going on
	step := func(at choiceui.Step) (bool, error) {
		seen = append(seen, fmt.Sprintf("%d/%d", at.At, at.Of))
		if backs[at.At] > 0 {
			backs[at.At]--
			return true, nil
		}
		return false, nil
	}
	if err := runInitSteps([]initStep{step, step, step}); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(seen, " "), "1/3 2/3 1/3 2/3 3/3 2/3 3/3"; got != want {
		t.Errorf("screens visited %q, want %q", got, want)
	}
}

func TestInitStepsIgnoreABackFromTheFirstScreenAndStopOnAnError(t *testing.T) {
	calls := 0
	first := func(choiceui.Step) (bool, error) { calls++; return calls == 1, nil }
	boom := errors.New("cancelled")
	second := func(choiceui.Step) (bool, error) { return false, boom }
	if err := runInitSteps([]initStep{first, second}); !errors.Is(err, boom) {
		t.Errorf("runInitSteps = %v, want the second screen's error", err)
	}
	if calls != 1 {
		t.Errorf("the first screen ran %d times; a back from it has nowhere to go", calls)
	}
}

func TestARevisitKeepsAnswersButFollowsTheHostForTheInstall(t *testing.T) {
	plan := sbx.Plan{Prefix: "/home/u/.docker/sbx"}
	def := defaultDecisions(plan, "0.50.0", initOptions{})
	prev := &initDecisions{Install: installTarball, Prefix: "/usr/local", Login: false, Wizard: false, Gh: true, Git: false, Baseline: sbx.BaselineDenyAll}
	got := carried(def, prev)
	if got.Install != installSkip || got.Prefix != plan.Prefix {
		t.Errorf("install/prefix = %q/%q, want what the host has now: %q/%q", got.Install, got.Prefix, installSkip, plan.Prefix)
	}
	if got.Login || got.Wizard || !got.Gh || got.Git || got.Baseline != sbx.BaselineDenyAll {
		t.Errorf("the revisit dropped the operator's answers: %+v", got)
	}
	if carried(def, nil) != def {
		t.Error("a first visit must open on the defaults")
	}
}
