// SPEC: _spec/internal/sbx/host-inference.puml
package run

import (
	"errors"
	"fmt"
	"os"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// admitOversizedModel turns a *sbx.NoFitError into a warning the operator may
// wave through, with notes on what the run will be like; every other error stands.
func admitOversizedModel(err error, notes []string) error {
	var nofit *sbx.NoFitError
	if !errors.As(err, &nofit) {
		return err
	}
	if !confirmOversizedModel(nofit, notes) {
		return err
	}
	ui.Warnf("local model %v; loading it anyway", nofit)
	for _, n := range notes {
		ui.Hostf("%s", n)
	}
	return nil
}

// localModelNotes sets expectations for a host model run: the context window the
// harness needs and where to watch the speed.
func localModelNotes(lm manifest.LocalModel, harness, served string) []string {
	var notes []string
	if lm.Context > 0 {
		notes = append(notes, fmt.Sprintf("%s needs a %dK-token context; this run serves it as %s.", harness, lm.Context/1024, served))
	}
	if lm.RateHint != "" {
		notes = append(notes, "Speed: "+lm.RateHint+".")
	}
	return notes
}

// confirmOversizedModel asks on a headed terminal; a run nobody can answer proceeds.
var confirmOversizedModel = func(e *sbx.NoFitError, notes []string) bool {
	if scheduled() || !agentio.WizardEnabled() || !agentio.IsStdinTTY() || !agentio.IsWriterTTY(os.Stdout) {
		return true
	}
	const gb = 1 << 30
	yes, asked := choiceui.AskOversizedModel(choiceui.ModelFit{
		Model: e.Model, Pool: e.Pool, Consequence: e.Consequence(), Breakdown: e.Breakdown(),
		Need: float64(e.Need) / gb, Free: float64(e.Free) / gb, Notes: notes,
		ForceVar: sbx.EnvLocalModelForce,
	})
	return yes || !asked
}
