// SPEC: _spec/internal/sbx/host-inference.puml
package choiceui

import (
	"slices"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

var qwenOnVRAM = ModelFit{
	Model: "qwen3.8:latest", Pool: "VRAM", Consequence: "Ollama puts ~74% on the GPU and the other ~5.0 GiB on the CPU, several times slower",
	Need: 19.6, Weights: 16.5, Free: 14.6, ForceVar: "PROVEO_LOCAL_MODEL_FORCE",
}

func TestOversizedModelCopyNamesPoolCostAndOverride(t *testing.T) {
	got := strings.Join(OversizedModelCopy(qwenOnVRAM), "\n")
	for _, want := range []string{
		"qwen3.8:latest does not fit in VRAM", "~19.6 GiB", "16.5 GiB weights", "14.6 GiB of VRAM",
		"~74% on the GPU", "PROVEO_LOCAL_MODEL_FORCE=1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("copy lacks %q:\n%s", want, got)
		}
	}
}

func confirmOversized(t *testing.T, keys ...*tcell.EventKey) bool {
	t.Helper()
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sim.Fini)
	sim.SetSize(100, 36)
	for _, ev := range keys {
		sim.InjectKey(ev.Key(), ev.Rune(), ev.Modifiers())
	}
	return ConfirmOversizedModel(sim, qwenOnVRAM)
}

func TestConfirmOversizedModelDefaultsToStop(t *testing.T) {
	if !confirmOversized(t, key('y'), keyEnter()) {
		t.Error("y then enter must load")
	}
	for name, keys := range map[string][]*tcell.EventKey{
		"empty enter": {keyEnter()},
		"n":           {key('n'), keyEnter()},
		"esc":         {tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)},
	} {
		if confirmOversized(t, keys...) {
			t.Errorf("%s loaded the model", name)
		}
	}
	if !slices.ContainsFunc(oversizedModelChoices(), func(c modalChoice) bool { return c.Key == "y/N" }) {
		t.Error("the prompt must show No as the default")
	}
}
