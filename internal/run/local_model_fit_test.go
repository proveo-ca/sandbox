// SPEC: _spec/internal/sbx/host-inference.puml
package run

import (
	"errors"
	"fmt"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

func stubConfirmOversized(t *testing.T, answer bool) *int {
	t.Helper()
	asked := 0
	orig := confirmOversizedModel
	t.Cleanup(func() { confirmOversizedModel = orig })
	confirmOversizedModel = func(*sbx.NoFitError) bool { asked++; return answer }
	return &asked
}

func TestAdmitOversizedModelLetsTheOperatorProceed(t *testing.T) {
	nofit := fmt.Errorf("wrapped: %w", &sbx.NoFitError{Model: "qwen3.8:latest", Pool: "VRAM"})

	stubConfirmOversized(t, true)
	if err := admitOversizedModel(nofit); err != nil {
		t.Errorf("yes must load the model: %v", err)
	}

	stubConfirmOversized(t, false)
	if err := admitOversizedModel(nofit); !errors.Is(err, nofit) {
		t.Errorf("no must stop the run with the fit report, got %v", err)
	}
}

func TestAdmitOversizedModelNeverAsksAboutOtherErrors(t *testing.T) {
	asked := stubConfirmOversized(t, true)
	missing := errors.New("no Ollama answers on 127.0.0.1:11434")
	if err := admitOversizedModel(missing); !errors.Is(err, missing) || *asked != 0 {
		t.Errorf("a missing server is not a fit question: err=%v asked=%d", err, *asked)
	}
}

func TestHeadlessRunLoadsAnOversizedModel(t *testing.T) {
	t.Setenv("PROVEO_SCHEDULE", "muse-lineup")
	if !confirmOversizedModel(&sbx.NoFitError{Model: "muse-glimmer:latest", Pool: "VRAM"}) {
		t.Error("a scheduled run has nobody to answer; it must warn and proceed")
	}
}
