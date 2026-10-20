// SPEC: _spec/internal/runlog/run-transcript.puml
package run

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/ui"
)

func TestTheRetiredStateRootWarningReachesTheRunLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PROVEO_HOME", home)
	t.Setenv("PROVEO_STATE_ROOT", "")
	t.Setenv("PROVEO_EGRESS_ROOT", t.TempDir())
	warnedRetired.Delete("PROVEO_EGRESS_ROOT")
	t.Cleanup(func() { warnedRetired.Delete("PROVEO_EGRESS_ROOT") })

	prev := ui.Default
	ui.Default = ui.New(io.Discard)
	t.Cleanup(func() { ui.Default = prev })

	stop := errors.New("no such harness")
	if err := Do(Params{Target: "nope"}, Deps{
		ManifestFor: func(string) (manifest.Manifest, error) { return manifest.Manifest{}, stop },
	}); !errors.Is(err, stop) {
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
	if !strings.Contains(string(b), "PROVEO_EGRESS_ROOT is deprecated") {
		t.Errorf("the transcript misses the retired-variable warning:\n%s", b)
	}
}
