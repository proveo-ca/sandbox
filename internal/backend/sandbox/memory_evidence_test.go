package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/runlog"
)

func withGuestSaying(t *testing.T, out string, err error) {
	t.Helper()
	prev := memoryEvidence
	memoryEvidence = func(string) ([]byte, error) { return []byte(out), err }
	t.Cleanup(func() { memoryEvidence = prev })
}

// SPEC: _spec/minimum_requirements.puml
func TestTeardownRecordsWhatTheGuestSaidAboutMemory(t *testing.T) {
	withGuestSaying(t, "== meminfo ==\nMemTotal: 12157280 kB\nSwapTotal: 0 kB\n", nil)
	dir := t.TempDir()
	CaptureMemoryEvidence(dir, "proveo-1")

	b, err := os.ReadFile(filepath.Join(dir, "sbx", runlog.MemoryEvidenceFile))
	if err != nil {
		t.Fatalf("teardown wrote no memory record: %v — a sandbox that died of memory "+
			"pressure leaves no OOMKilled flag and no exit message, so this file is the "+
			"only place an operator can read the cause", err)
	}
	if !strings.Contains(string(b), "SwapTotal") {
		t.Errorf("record does not carry the guest's own numbers:\n%s", b)
	}
}

// The capture must never itself become the story. A run that died of memory
// pressure is the run least able to answer the question.
func TestTeardownIsSilentWhenTheGuestCannotAnswer(t *testing.T) {
	for _, c := range []struct{ name, out string }{
		{"sandbox already gone", ""},
		{"whitespace only", "   \n\t\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			withGuestSaying(t, c.out, os.ErrNotExist)
			dir := t.TempDir()
			CaptureMemoryEvidence(dir, "proveo-1")
			if _, err := os.Stat(filepath.Join(dir, "sbx", runlog.MemoryEvidenceFile)); err == nil {
				t.Error("an empty answer was written as evidence — a file that says nothing " +
					"is worse than none, because it looks like it was checked")
			}
		})
	}
	t.Run("no sandbox name", func(t *testing.T) {
		withGuestSaying(t, "MemTotal: 1 kB", nil)
		dir := t.TempDir()
		CaptureMemoryEvidence(dir, "")
		if _, err := os.Stat(filepath.Join(dir, "sbx")); err == nil {
			t.Error("wrote a record for a sandbox with no name")
		}
	})
}

// Output that arrives WITH an error is still evidence: `docker ps` failing is a
// symptom, and the meminfo above it is the part that matters.
func TestPartialOutputIsStillRecorded(t *testing.T) {
	withGuestSaying(t, "== meminfo ==\nMemTotal: 12157280 kB\n(docker did not answer)\n", os.ErrDeadlineExceeded)
	dir := t.TempDir()
	CaptureMemoryEvidence(dir, "proveo-1")
	if _, err := os.Stat(filepath.Join(dir, "sbx", runlog.MemoryEvidenceFile)); err != nil {
		t.Errorf("partial output was discarded: %v — a wedged docker daemon is itself the "+
			"finding, and dropping the record loses the meminfo with it", err)
	}
}
