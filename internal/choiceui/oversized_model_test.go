// SPEC: _spec/internal/sbx/host-inference.puml
package choiceui

import (
	"slices"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

var qwenOnVRAM = ModelFit{
	Model: "qwen3.8:latest", Pool: "VRAM", Consequence: "Ollama puts ~35% on the GPU and the other ~13.9 GiB on the CPU, several times slower",
	Breakdown: "16.5 GiB weights + 4.0 GiB for a 65,536-token context + 1 GiB runtime",
	Need:      21.5, Free: 7.6, ForceVar: "PROVEO_LOCAL_MODEL_FORCE",
	Notes: []string{"hermes needs a 65,536-token context; this run serves qwen3.8:latest-ctx64k.",
		"Hermes Desktop: right-click the status bar → Tokens per second"},
}

func TestOversizedModelCopyNamesPoolCostAndOverride(t *testing.T) {
	got := strings.Join(OversizedModelCopy(qwenOnVRAM), "\n")
	for _, want := range []string{
		"qwen3.8:latest does not fit in VRAM", "~21.5 GiB", "4.0 GiB for a", "65,536-token context", "7.6 GiB of VRAM",
		"~35% on the GPU", "qwen3.8:latest-ctx64k", "Tokens per second", "PROVEO_LOCAL_MODEL_FORCE=1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("copy lacks %q:\n%s", want, got)
		}
	}
}

func TestOversizedModelCopyFitsAnEightyColumnTerminal(t *testing.T) {
	for _, l := range OversizedModelCopy(qwenOnVRAM) {
		if n := len([]rune(l)); n > oversizedModelWidth {
			t.Errorf("%d runes > %d: %q", n, oversizedModelWidth, l)
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
