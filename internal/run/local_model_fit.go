// SPEC: _spec/internal/sbx/host-inference.puml
package run

import (
	"errors"
	"os"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// admitOversizedModel turns a *sbx.NoFitError into a warning the operator may
// wave through; every other error stands.
func admitOversizedModel(err error) error {
	var nofit *sbx.NoFitError
	if !errors.As(err, &nofit) {
		return err
	}
	if !confirmOversizedModel(nofit) {
		return err
	}
	ui.Warnf("local model %v; loading it anyway", nofit)
	return nil
}

// confirmOversizedModel asks on a headed terminal; a run nobody can answer proceeds.
var confirmOversizedModel = func(e *sbx.NoFitError) bool {
	if scheduled() || !agentio.WizardEnabled() || !agentio.IsStdinTTY() || !agentio.IsWriterTTY(os.Stdout) {
		return true
	}
	const gb = 1 << 30
	yes, asked := choiceui.AskOversizedModel(choiceui.ModelFit{
		Model: e.Model, Pool: e.Pool, Consequence: e.Consequence(),
		Need: float64(e.Need) / gb, Weights: float64(e.Weights) / gb, Free: float64(e.Free) / gb,
		ForceVar: sbx.EnvLocalModelForce,
	})
	return yes || !asked
}
