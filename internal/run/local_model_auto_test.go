// SPEC: _spec/internal/sbx/host-inference.puml (BEST HOST MODEL)
package run

import (
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

func standardCapacity() uint64 { return 24 << 30 }

func stubFitting(t *testing.T, fit ...string) *[]string {
	t.Helper()
	var asked []string
	orig := fittingHostModels
	t.Cleanup(func() { fittingHostModels = orig })
	fittingHostModels = func(c []string, _ int, _ func(string) string) []string { asked = c; return fit }
	return &asked
}

func TestAutoLocalModelPicksTheBestFittingListedModel(t *testing.T) {
	asked := stubFitting(t, qwen.tag(), glimmer.tag())
	rs := &Spec{Man: manifest.Manifest{Name: "hermes"}}
	if got := autoLocalModel(rs, &Params{Target: "hermes"}, func(string) string { return "" }, standardCapacity); got != qwen.tag() {
		t.Errorf("autoLocalModel(no key, no model) = %q, want %q", got, qwen.tag())
	}
	if len(*asked) != 2 {
		t.Errorf("candidates = %v, want the two 24 GB presets", *asked)
	}
}

func TestAutoLocalModelLeavesAnExplicitChoiceAlone(t *testing.T) {
	stubFitting(t, localModels[0].tag())
	none := func(string) string { return "" }
	for _, c := range []struct {
		name   string
		rs     *Spec
		p      *Params
		getenv func(string) string
	}{
		{"local model given", &Spec{Man: manifest.Manifest{Name: "hermes"}}, &Params{Target: "hermes", LocalModel: "x:1"}, none},
		{"provider key", func() *Spec {
			s := &Spec{Man: manifest.Manifest{Name: "hermes"}}
			s.Creds.Detected = []string{"openrouter"}
			return s
		}(), &Params{Target: "hermes"}, none},
		{"subscription", &Spec{Man: manifest.Manifest{Name: "claudecode", Subscription: true}}, &Params{Target: "claudecode"}, none},
		{"vendor-pinned", &Spec{Man: manifest.Manifest{Name: "cursor"}}, &Params{Target: "cursor"}, none},
		{"print only", &Spec{Man: manifest.Manifest{Name: "hermes"}}, &Params{Target: "hermes", PrintOnly: true}, none},
		{"opted out", &Spec{Man: manifest.Manifest{Name: "hermes"}}, &Params{Target: "hermes"},
			func(k string) string { return map[string]string{"PROVEO_LOCAL_MODEL_AUTO": "0"}[k] }},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := autoLocalModel(c.rs, c.p, c.getenv, standardCapacity); got != "" {
				t.Errorf("autoLocalModel(%s) = %q, want none", c.name, got)
			}
		})
	}
}

func TestAutoLocalModelNeedsAFit(t *testing.T) {
	stubFitting(t)
	if got := autoLocalModel(&Spec{Man: manifest.Manifest{Name: "hermes"}}, &Params{Target: "hermes"}, func(string) string { return "" }, standardCapacity); got != "" {
		t.Errorf("autoLocalModel with nothing fitting = %q, want none", got)
	}
}

func TestAutoLocalModelChecksOnlyTheActiveTier(t *testing.T) {
	want := []string{localModelFor("Nemotron"), localModelFor("Flash Next")}
	asked := stubFitting(t, want[1])
	rs := &Spec{Man: manifest.Manifest{Name: "hermes"}}
	p := &Params{Target: "hermes"}
	getenv := func(string) string { return "" }
	if got := autoLocalModel(rs, p, getenv, func() uint64 { return 96 << 30 }); got != want[1] {
		t.Errorf("96 GB auto-pick = %q, want %q", got, want[1])
	}
	if !slices.Equal(*asked, want) {
		t.Errorf("fit candidates = %v, want %v", *asked, want)
	}
	*asked = nil
	if got := autoLocalModel(rs, p, getenv, func() uint64 { return 16 << 30 }); got != "" || *asked != nil {
		t.Errorf("16 GB auto-pick = %q, candidates = %v", got, *asked)
	}
}

func TestTheAutoLocalModelsReasonReachesOnlyTheRunLog(t *testing.T) {
	stubFitting(t, localModels[0].tag())
	var screen, log strings.Builder
	prev := ui.Default
	ui.Default = ui.New(&screen)
	restore := ui.TeeTo(&log)
	t.Cleanup(func() { restore(); ui.Default = prev })
	if got := autoLocalModel(&Spec{Man: manifest.Manifest{Name: "hermes"}}, &Params{Target: "hermes"}, func(string) string { return "" }, standardCapacity); got == "" {
		t.Fatal("autoLocalModel picked nothing")
	}
	if got := screen.String(); got != "" {
		t.Errorf("the auto pick printed %q on screen; the egress block's local model fact is its one line", got)
	}
	for _, want := range []string{"no provider key", sbx.EnvLocalModelAuto + "=0"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the run log lacks %q:\n%s", want, log.String())
		}
	}
}

func TestAutoLocalModelAsksForTheRowsLineup(t *testing.T) {
	stubOllamaHost(t, "darwin/arm64", "0.40.17")
	asked := stubFitting(t)
	autoLocalModel(&Spec{Man: manifest.Manifest{Name: "hermes"}}, &Params{Target: "hermes"}, func(string) string { return "" }, standardCapacity)
	if want := []string{"qwen3.8:27b-mlx", "inkling-2-mini:26b-a5b-nvfp4"}; !slices.Equal(*asked, want) {
		t.Errorf("candidates = %v, want %v", *asked, want)
	}
}
