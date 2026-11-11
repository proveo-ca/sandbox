// SPEC: _spec/internal/sbx/host-inference.puml
package choiceui

import (
	"strings"
	"testing"
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
