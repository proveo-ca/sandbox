//go:build !windows

// SPEC: _spec/internal/backend/teardown.puml
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// quietTeardown builds a teardown for sandbox sb against a fake sbx and returns the terminal it prints to.
func quietTeardown(t *testing.T, failed bool) (*teardown, *bytes.Buffer, string) {
	t.Helper()
	calls := fakeSbx(t, guestClone, 0, bundleEmpty)
	var term bytes.Buffer
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	ui.Default = ui.New(&term)
	now := time.Now()
	return &teardown{
		in:         Input{CloneKnown: true},
		cfg:        sbx.RunConfig{Name: "sb"},
		failed:     failed,
		started:    now,
		ended:      now,
		lastOutput: func() []string { return []string{"the agent's last line"} },
		commitHome: func() error { return nil },
	}, &term, calls
}

// topLevelSteps drops the clone step's sub-steps.
func topLevelSteps(steps []recordedStep) []string {
	var names []string
	for _, s := range steps {
		if !strings.Contains(s.name, "/") {
			names = append(names, s.name)
		}
	}
	return names
}

func sbxCalls(t *testing.T, calls string) string {
	t.Helper()
	b, err := os.ReadFile(calls)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

func TestATeardownRunRecordsExactlyItsPlan(t *testing.T) {
	for _, c := range []struct {
		name     string
		failed   bool
		removes  bool
		keepHome bool
	}{
		{"a normal exit", false, true, false},
		{"a failed run", true, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := captureSteps(t)
			td, _, calls := quietTeardown(t, c.failed)
			td.run(context.Background())
			if names := topLevelSteps(*got); !slices.Equal(names, teardownPlan(c.failed)) {
				t.Errorf("recorded %v\nplan     %v", names, teardownPlan(c.failed))
			}
			for _, s := range *got {
				if s.err != nil {
					t.Errorf("step %q failed: %v", s.name, s.err)
				}
			}
			if removed := strings.Contains(sbxCalls(t, calls), "rm --force sb"); removed != c.removes {
				t.Errorf("sandbox removed = %v, want %v", removed, c.removes)
			}
			if td.keepHome != c.keepHome {
				t.Errorf("keepHome = %v, want %v", td.keepHome, c.keepHome)
			}
		})
	}
}

func TestTheFailedPlanIsTheNormalPlanWithoutTheRemoval(t *testing.T) {
	normal, failed := teardownPlan(false), teardownPlan(true)
	if last := normal[len(normal)-1]; last != stepRemove {
		t.Errorf("a normal exit ends on %q, want %q", last, stepRemove)
	}
	if !slices.Equal(failed, normal[:len(normal)-1]) {
		t.Errorf("failed plan %v, want the normal plan without its last step", failed)
	}
}

func TestAPanickingStepBecomesItsErrorAndTheNextStepsStillRun(t *testing.T) {
	got := captureSteps(t)
	td, term, calls := quietTeardown(t, false)
	td.commitHome = func() error { panic("home store gone") }
	td.run(context.Background())
	if names := topLevelSteps(*got); !slices.Equal(names, teardownPlan(false)) {
		t.Fatalf("recorded %v, want the whole plan %v", names, teardownPlan(false))
	}
	for _, s := range *got {
		if s.name == stepHome && (s.err == nil || !strings.Contains(s.err.Error(), "home store gone")) {
			t.Errorf("home step error = %v, want the panic's value", s.err)
		}
	}
	if !strings.Contains(term.String(), "home-root config not preserved") {
		t.Errorf("the operator was not told the home config was lost:\n%s", term.String())
	}
	if !strings.Contains(sbxCalls(t, calls), "rm --force sb") {
		t.Error("the sandbox was not removed after the panic")
	}
}

func TestAStoppedTeardownSkipsItsStepsAndKeepsTheSandbox(t *testing.T) {
	got := captureSteps(t)
	td, term, calls := quietTeardown(t, false)
	ran := false
	td.commitHome = func() error { ran = true; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	td.run(ctx)
	want := teardownPlan(true)
	if names := topLevelSteps(*got); !slices.Equal(names, want) {
		t.Fatalf("recorded %v, want %v", names, want)
	}
	for _, s := range *got {
		if !errors.Is(s.err, errStepSkipped) {
			t.Errorf("step %q outcome = %v, want skipped", s.name, s.err)
		}
	}
	if ran {
		t.Error("a step ran after teardown was stopped")
	}
	if strings.Contains(sbxCalls(t, calls), "rm --force") {
		t.Error("a stopped teardown removed the sandbox")
	}
	if !td.keepHome {
		t.Error("keepHome = false for a sandbox that outlives the run")
	}
	if !strings.Contains(term.String(), "proveo rm sb") {
		t.Errorf("the kept sandbox's removal command is missing:\n%s", term.String())
	}
}

func TestAStopEndsTheGuestCallInFlight(t *testing.T) {
	slowSbx(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	err := removeSandboxQuiet(t, ctx)
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("the removal took %s after a stop", took)
	}
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want a stop, not a deadline", err)
	}
}

func removeSandboxQuiet(t *testing.T, ctx context.Context) error {
	t.Helper()
	var term bytes.Buffer
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	ui.Default = ui.New(&term)
	return removeSandbox(ctx, "sb")
}

func TestEveryPlannedStepStartsWithItsNameAndCeilingOnTheTerminal(t *testing.T) {
	captureSteps(t)
	td, term, _ := quietTeardown(t, false)
	td.run(context.Background())
	at := 0
	for _, name := range teardownPlan(false) {
		if name == stepClone {
			continue
		}
		line := name
		if d := stepCeiling(name); d > 0 {
			line = name + " — up to " + d.Round(time.Second).String()
		}
		i := strings.Index(term.String()[at:], line)
		if i < 0 {
			t.Fatalf("no start line %q after offset %d:\n%s", line, at, term.String())
		}
		at += i + len(line)
	}
}

func TestStepCeilingsAddUpTheBoundsOfTheCallsAStepMakes(t *testing.T) {
	for name, want := range map[string]time.Duration{
		stepCredential:    34 * time.Second,
		stepCloneMode:     38 * time.Second,
		stepPushed:        3*time.Minute + 14*time.Second,
		stepEgress:        12 * time.Second,
		stepMemory:        24 * time.Second,
		stepCloneEnv:      3*time.Minute + 14*time.Second,
		stepCloneSnapshot: 3*time.Minute + 2*time.Second,
		stepCloneOutput:   3*time.Minute + 2*time.Second,
		stepCloneCarry:    12*time.Minute + 20*time.Second,
		stepCloneSpec:     21*time.Minute + 14*time.Second,
		stepState:         3*time.Minute + 14*time.Second,
		stepHome:          0,
		stepRemove:        3*time.Minute + 2*time.Second,
	} {
		if got := stepCeiling(name); got != want {
			t.Errorf("stepCeiling(%q) = %s, want %s", name, got, want)
		}
	}
	shrinkGuestDeadlines(t, time.Second)
	if got := stepCeiling(stepRemove); got != time.Second+guestGrace {
		t.Errorf("a shortened deadline left the removal's ceiling at %s", got)
	}
}

func TestAStepFrameCarriesItsPlaceItsClockAndItsCeiling(t *testing.T) {
	t.Cleanup(func() { placeNow.Store(nil) })
	if got, want := stepFrame(stepCloneSnapshot, 12*time.Second), "clone/snapshot · 12s of up to 3m2s"; got != want {
		t.Errorf("outside teardown: frame = %q, want %q", got, want)
	}
	placeNow.Store(&stepPlace{n: 6, of: 9})
	if got, want := stepFrame(stepCloneSnapshot, 12*time.Second), "step 6/9 · clone/snapshot · 12s of up to 3m2s"; got != want {
		t.Errorf("in teardown: frame = %q, want %q", got, want)
	}
	if got, want := stepFrame(stepHome, 0), "step 6/9 · home config · 0s"; got != want {
		t.Errorf("a step with no ceiling: frame = %q, want %q", got, want)
	}
}

func TestATerminalGetsALiveLinePerStepAndNoStartLines(t *testing.T) {
	captureSteps(t)
	td, _, _ := quietTeardown(t, false)
	term := &lockedBuffer{}
	ui.Default = ui.NewTerminal(term, 120)
	clock := stepClock
	t.Cleanup(func() { stepClock = clock })
	stepClock = 5 * time.Millisecond
	slow := make(chan struct{})
	td.commitHome = func() error { <-slow; return nil }
	done := make(chan struct{})
	go func() { defer close(done); td.run(context.Background()) }()
	term.await(t, "step 8/9 · home config · 0s")
	for deadline := time.Now().Add(5 * time.Second); strings.Count(term.String(), "step 8/9 · home config") < 3; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the running step's line was not redrawn on its clock:\n%q", term.String())
		}
	}
	close(slow)
	<-done

	screen := term.String()
	at := 0
	for n, name := range teardownPlan(false) {
		if name == stepClone {
			continue
		}
		frame := fmt.Sprintf("step %d/9 · %s · ", n+1, name)
		i := strings.Index(screen[at:], frame)
		if i < 0 {
			t.Fatalf("no live line %q after offset %d:\n%q", frame, at, screen)
		}
		at += i
	}
	if strings.Contains(screen, " — up to ") {
		t.Errorf("a terminal received a start line as well as the live line:\n%q", screen)
	}
	if !strings.HasSuffix(screen, "\r\x1b[K") {
		t.Errorf("teardown left its live line on the terminal: %q", screen[max(0, len(screen)-80):])
	}
}
