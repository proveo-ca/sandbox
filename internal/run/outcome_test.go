package run

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/runlog"
	"github.com/proveo-ca/proveo/internal/ui"
)

// SPEC: _spec/internal/runlog/run-transcript.puml
func TestRecordOutcomeWritesTheVerdictIntoTheTranscript(t *testing.T) {
	cases := []struct {
		name     string
		launched bool
		err      error
		want     string
	}{
		{"an agent that exited non-zero", true, backend.ExitError{Code: 137}, "137"},
		{"a plain failure", true, errors.New("cursor has no --local-model path"), "cursor has no --local-model path"},
		{"success", true, nil, "exited 0"},
		// `--print` and the not-ready paths return nil without ever starting an
		// agent; recording "exited 0" there made the transcript unable to tell
		// a successful run from one that never launched.
		{"nothing was launched", false, nil, "no agent was launched"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("PROVEO_HOME", home)

			log, err := runlog.Open("proveo-test-" + strings.ReplaceAll(tc.name, " ", "-"))
			if err != nil {
				t.Fatal(err)
			}
			// Exactly what Do does, and the reason an exit code reaches the
			// transcript at all: ui writes to the terminal and the log together.
			prev := ui.Default
			ui.TeeTo(log.Writer())
			defer func() { ui.Default = prev }()

			recordOutcome(tc.launched, tc.err)
			log.Close()
			b, err := os.ReadFile(log.Path())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), tc.want) {
				t.Errorf("the transcript never says %q:\n%s", tc.want, b)
			}
		})
	}
}

func TestRecordOutcomeDoesNotDoubleReportAPlainError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PROVEO_HOME", home)
	log, err := runlog.Open("proveo-test-plain")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	var sb strings.Builder
	prev := ui.Default
	ui.Default = ui.New(&sb)
	defer func() { ui.Default = prev }()

	recordOutcome(true, errors.New("boom"))
	if strings.Contains(sb.String(), "boom") {
		t.Errorf("a plain error was printed here as well as by main:\n%s", sb.String())
	}

	recordOutcome(true, backend.ExitError{Code: 2})
	if !strings.Contains(sb.String(), "2") {
		t.Errorf("an exit code must reach the operator, not only the log:\n%s", sb.String())
	}
}

// A run whose log never opened still has to finish. ui.Logf is a no-op until
// TeeTo has run, which is exactly the state after runlog.Open failed.
func TestRecordOutcomeSurvivesAnUnopenedLog(t *testing.T) {
	recordOutcome(false, nil)
	recordOutcome(true, backend.ExitError{Code: 1})
}

func TestDoRecordsAnEarlyFailureInTheTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PROVEO_HOME", home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	prev := ui.Default
	var sink strings.Builder
	ui.Default = ui.New(&sink)
	defer func() { ui.Default = prev }()

	// ManifestFor is the first thing Do calls after the log opens, so nothing
	// else has to be stubbed for this to be a real early return.
	want := errors.New("no such harness")
	err := Do(Params{Target: "nope"}, Deps{
		ManifestFor: func(string) (manifest.Manifest, error) { return manifest.Manifest{}, want },
	})
	if !errors.Is(err, want) {
		t.Fatalf("Do = %v, want the manifest error", err)
	}

	logs, err := filepath.Glob(filepath.Join(home, "logs", "proveo-*.log"))
	if err != nil || len(logs) == 0 {
		t.Fatalf("no run log was written (%v)", err)
	}
	b, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "outcome: no such harness") {
		t.Errorf("the transcript never says how the run ended:\n%s", b)
	}
}
