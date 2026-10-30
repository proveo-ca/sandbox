// SPEC: _spec/internal/sbx/host-inference.puml
package choiceui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
)

// ModelFit is a local model the host's free memory cannot hold, in GiB.
type ModelFit struct {
	Model, Pool, Consequence string
	Need, Weights, Free      float64
	ForceVar                 string
}

func oversizedModelTitle(f ModelFit) string {
	return fmt.Sprintf("%s does not fit in %s", f.Model, f.Pool)
}

// OversizedModelCopy explains loading a local model larger than the pool Ollama loads it into.
func OversizedModelCopy(f ModelFit) []string {
	return []string{
		oversizedModelTitle(f),
		"",
		fmt.Sprintf("needs ~%.1f GiB (%.1f GiB weights + cache headroom)", f.Need, f.Weights),
		fmt.Sprintf("free   %.1f GiB of %s", f.Free, f.Pool),
		"",
		"Loading it anyway: " + f.Consequence + ".",
		f.ForceVar + "=1 skips this question.",
	}
}

func oversizedModelChoices() []modalChoice {
	return []modalChoice{
		{Key: "y/N", Text: "load it anyway"},
		{Key: "esc", Text: "stop — free memory or pick a smaller tag"},
	}
}

// ConfirmOversizedModel waits for permission to load a model that does not fit.
func ConfirmOversizedModel(s tcell.Screen, f ModelFit) bool {
	return confirmPrompt(s, OversizedModelCopy(f), oversizedModelTitle(f), oversizedModelChoices())
}

// AskOversizedModel opens the terminal and asks; asked is false when no screen opens.
func AskOversizedModel(f ModelFit) (yes, asked bool) {
	preferUTF8Locale()
	screen, err := tcell.NewScreen()
	if err != nil {
		return false, false
	}
	if err := screen.Init(); err != nil {
		return false, false
	}
	registerGlyphFallbacks(screen)
	defer screen.Fini()
	return ConfirmOversizedModel(screen, f), true
}
