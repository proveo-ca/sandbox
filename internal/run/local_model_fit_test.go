// SPEC: _spec/internal/sbx/host-inference.puml
package run

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func stubConfirmOversized(t *testing.T, answer bool) *int {
	t.Helper()
	asked := 0
	orig := confirmOversizedModel
	t.Cleanup(func() { confirmOversizedModel = orig })
	confirmOversizedModel = func(*sbx.NoFitError, []string) bool { asked++; return answer }
	return &asked
}

func TestAdmitOversizedModelLetsTheOperatorProceed(t *testing.T) {
	nofit := fmt.Errorf("wrapped: %w", &sbx.NoFitError{Model: "qwen3.8:latest", Pool: "VRAM"})

	stubConfirmOversized(t, true)
	if err := admitOversizedModel(nofit, nil); err != nil {
		t.Errorf("yes must load the model: %v", err)
	}

	stubConfirmOversized(t, false)
	if err := admitOversizedModel(nofit, nil); !errors.Is(err, nofit) {
		t.Errorf("no must stop the run with the fit report, got %v", err)
	}
}

func TestAdmitOversizedModelNeverAsksAboutOtherErrors(t *testing.T) {
	asked := stubConfirmOversized(t, true)
	missing := errors.New("no Ollama answers on 127.0.0.1:11434")
	if err := admitOversizedModel(missing, nil); !errors.Is(err, missing) || *asked != 0 {
		t.Errorf("a missing server is not a fit question: err=%v asked=%d", err, *asked)
	}
}

func TestHeadlessRunLoadsAnOversizedModel(t *testing.T) {
	t.Setenv("PROVEO_SCHEDULE", "muse-lineup")
	if !confirmOversizedModel(&sbx.NoFitError{Model: "muse-glimmer:latest", Pool: "VRAM"}, nil) {
		t.Error("a scheduled run has nobody to answer; it must warn and proceed")
	}
}

func TestLocalModelNotesSetExpectationsFromTheManifest(t *testing.T) {
	lm := manifest.LocalModel{Context: 65536, RateHint: "Hermes Desktop: right-click the status bar → Tokens per second"}
	got := strings.Join(localModelNotes(lm, "hermes", "qwen3.8:latest-ctx64k"), "\n")
	for _, want := range []string{"hermes needs a 64K-token context", "qwen3.8:latest-ctx64k", "Tokens per second"} {
		if !strings.Contains(got, want) {
			t.Errorf("notes lack %q:\n%s", want, got)
		}
	}
	if n := localModelNotes(manifest.LocalModel{}, "opencode", "qwen3.8:latest"); len(n) != 0 {
		t.Errorf("a harness without a floor or hint gets no notes: %v", n)
	}
}
