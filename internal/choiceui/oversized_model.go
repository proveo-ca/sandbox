// SPEC: _spec/internal/sbx/host-inference.puml, _spec/internal/choiceui/confirm-dialogs.puml
package choiceui

import "fmt"

// ModelFit is a local model the host's free memory cannot hold, in GiB; Notes set
// expectations for the run that loads it anyway.
type ModelFit struct {
	Model, Pool, Consequence, Breakdown string
	Need, Free                          float64
	Notes                               []string
	ForceVar                            string
}

const oversizedModelWidth = 64

func oversizedModelTitle(f ModelFit) string {
	return fmt.Sprintf("%s does not fit in %s", f.Model, f.Pool)
}

// OversizedModelCopy explains loading a local model larger than the pool Ollama loads it into.
func OversizedModelCopy(f ModelFit) []string {
	lines := []string{oversizedModelTitle(f), ""}
	lines = append(lines, wrap(fmt.Sprintf("needs ~%.1f GiB (%s)", f.Need, f.Breakdown), oversizedModelWidth, 7)...)
	lines = append(lines, fmt.Sprintf("free   %.1f GiB of %s", f.Free, f.Pool), "")
	lines = append(lines, wrap("Loading it anyway: "+f.Consequence+".", oversizedModelWidth, 0)...)
	for _, n := range f.Notes {
		lines = append(lines, wrap(n, oversizedModelWidth, 0)...)
	}
	return append(lines, f.ForceVar+"=1 skips this question.")
}

// AskOversizedModel asks on the operator's terminal whether to load a model that does not
// fit; asked is false when no terminal opens.
func AskOversizedModel(f ModelFit) (yes, asked bool) {
	answer, asked := askDialog(oversizedModelDialog(f))
	return asked && answer == 1, asked
}

func oversizedModelDialog(f ModelFit) dialog {
	return newDialog(oversizedModelTitle(f), OversizedModelCopy(f), 0,
		dialogButton{Label: "Stop", Key: 's'},
		dialogButton{Label: "Load anyway", Key: 'l'},
	)
}
