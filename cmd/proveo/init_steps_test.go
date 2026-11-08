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

// A revisit of setup must not re-run what already ran: every one-shot action opens on later.
func TestARevisitOpensOnWhatIsLeft(t *testing.T) {
	plan := sbx.Plan{Prefix: "/home/u/.docker/sbx"}
	def := defaultDecisions(plan, "0.50.0", initOptions{})
	prev := &initDecisions{Install: installTarball, Prefix: "/usr/local", AddPath: false,
		Login: true, Wizard: true, Gh: true, Git: true, Baseline: sbx.BaselineDenyAll}
	got := carried(def, prev)
	if got.Install != installSkip || got.Prefix != plan.Prefix {
		t.Errorf("install/prefix = %q/%q, want what the host has now: %q/%q", got.Install, got.Prefix, installSkip, plan.Prefix)
	}
	if got.Login || got.Wizard || got.Gh || got.Git || got.Baseline != "" {
		t.Errorf("a revisit would re-run sbx login, the wizard, gh, git or the baseline: %+v", got)
	}
	if got.AddPath {
		t.Error("the PATH answer was not kept")
	}
	if carried(def, nil) != def {
		t.Error("a first visit must open on the defaults")
	}
}
