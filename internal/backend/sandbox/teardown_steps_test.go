//go:build !windows

// SPEC: _spec/internal/backend/teardown.puml
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

type recordedStep struct {
	name string
	took time.Duration
	err  error
}

// captureSteps routes recorded steps into the returned slice for one test.
func captureSteps(t *testing.T) *[]recordedStep {
	t.Helper()
	var got []recordedStep
	sink := func(name string, took time.Duration, err error) {
		got = append(got, recordedStep{name, took, err})
	}
	prev := stepSink.Swap(&sink)
	t.Cleanup(func() { stepSink.Store(prev) })
	return &got
}

func stepNames(steps []recordedStep) []string {
	var names []string
	for _, s := range steps {
		names = append(names, s.name)
	}
	return names
}

func TestAStepIsRecordedOnceWithItsNameDurationAndOutcome(t *testing.T) {
	got := captureSteps(t)
	boom := errors.New("boom")
	if err := step("first", func() error { return nil }); err != nil {
		t.Errorf("step returned %v for a step that succeeded", err)
	}
	if err := step("second", func() error { return boom }); !errors.Is(err, boom) {
		t.Errorf("step returned %v, want the step's own error", err)
	}
	stepDo("third", func() {})
	if names := stepNames(*got); !slices.Equal(names, []string{"first", "second", "third"}) {
		t.Fatalf("recorded %v, want each step once and in call order", names)
	}
	for _, s := range *got {
		if s.took < 0 {
			t.Errorf("step %q took %s", s.name, s.took)
		}
	}
	if (*got)[0].err != nil || !errors.Is((*got)[1].err, boom) {
		t.Errorf("outcomes = %v, %v", (*got)[0].err, (*got)[1].err)
	}
}

func TestAnUnsunkStepReachesNeitherTheTerminalNorAnUnopenedLog(t *testing.T) {
	var term bytes.Buffer
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	ui.Default = ui.New(&term)
	_ = step("quiet", func() error { return errors.New("line one\nline two") })
	if term.Len() != 0 {
		t.Errorf("a step printed to the terminal: %q", term.String())
	}
}

func TestTeardownCloneRecordsItsSubStepsInOrder(t *testing.T) {
	got := captureSteps(t)
	fakeSbx(t, guestClone, 0, bundleEmpty)
	repo := t.TempDir()
	in := Input{Clone: true, CloneKnown: true, RepoRoot: repo, Sid: "proveo-1-2"}
	cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: repo}}}
	_ = TeardownClone(in, cfg, nil)
	want := []string{stepCloneEnv, stepCloneSnapshot, stepCloneOutput, stepCloneCarry}
	if names := stepNames(*got); !slices.Equal(names, want) {
		t.Errorf("TeardownClone recorded %v, want %v", names, want)
	}
}

// slowSbx puts an sbx on PATH that lists sb and sleeps on rm, state save and reflog.
func slowSbx(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  ls*) printf 'SANDBOX AGENT STATUS\\nsb claude running\\n' ;;\n" +
		"  rm*|*proveo_sync_state*|*'log -g'*) sleep 30 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, sbx.Binary), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestTeardownDeadlineEndsEveryGuestCallThatNeverAnswers(t *testing.T) {
	shrinkGuestDeadlines(t, 300*time.Millisecond)
	slowSbx(t)
	var term bytes.Buffer
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	ui.Default = ui.New(&term)

	for _, c := range []struct {
		name string
		call func() error
	}{
		{"the state save", func() error {
			_, err := SaveState("sb", []string{sbx.StateHomeVar + "=/state"}, true, guestRun)
			return err
		}},
		{"the reflog read", func() error { _, err := sandboxReflog(context.Background(), "sb", "/work"); return err }},
		{"the removal", func() error { return removeSandbox(context.Background(), "sb") }},
	} {
		start := time.Now()
		err := c.call()
		if took := time.Since(start); took > 10*time.Second {
			t.Errorf("%s took %s; the deadline must end the wait", c.name, took)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s error = %v, want one that wraps context.DeadlineExceeded", c.name, err)
		}
	}
	if term.String() != "" {
		t.Errorf("a guest call printed on its own; teardown's results block says what failed:\n%s", term.String())
	}
}

func TestARemovalThatTimesOutClosesOnTheRescueLine(t *testing.T) {
	shrinkGuestDeadlines(t, 300*time.Millisecond)
	slowSbx(t)
	var term bytes.Buffer
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	ui.Default = ui.New(&term)
	captureSteps(t)
	now := time.Now()
	td := &teardown{in: Input{CloneKnown: true}, cfg: sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: "/work"}}}, started: now, ended: now,
		lastOutput: func() []string { return nil }, commitHome: func() error { return nil }}
	td.run(context.Background())
	got := term.String()
	for _, want := range []string{"warn: sb — kept: its removal failed", "check `sbx ls`, then `sbx rm --force sb` once sbx answers", "------ ide ------"} {
		if !strings.Contains(got, want) {
			t.Errorf("a removal that timed out does not close on %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "proveo rm") {
		t.Errorf("a removal that may be half-done names the rescue line, not `proveo rm`:\n%s", got)
	}
}

func TestAHungReflogAsksNoPushedBranchQuestion(t *testing.T) {
	shrinkGuestDeadlines(t, 300*time.Millisecond)
	slowSbx(t)
	asked := false
	prev := confirmFetchPushed
	t.Cleanup(func() { confirmFetchPushed = prev })
	confirmFetchPushed = func([]string) bool { asked = true; return false }
	repo := t.TempDir()
	in := Input{Clone: true, RepoRoot: repo}
	cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: repo}}}
	start := time.Now()
	reportPushedBranches(in, cfg, time.Now(), nil)
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("reportPushedBranches took %s behind a reflog that never answered", took)
	}
	if asked {
		t.Error("a reflog that never answered still asked the pushed-branch question")
	}
}
