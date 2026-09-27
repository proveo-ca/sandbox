// SPEC: _spec/internal/choiceui/wireframe.puml, _spec/defs/hermes/hermes-paradigm.puml
package run

import (
	"slices"
	"testing"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/manifest"
)

func hermesImagesMan() manifest.Manifest {
	return manifest.Manifest{Name: "hermes", Images: map[string]string{
		"hermes":              "proveo/hermes:latest",
		"hermes-muse-glimmer": "proveo/hermes-muse-glimmer:latest",
		"hermes-qwen3.8":      "proveo/hermes-qwen3.8:latest",
	}}
}

func selected(t *testing.T, localModel string) string {
	t.Helper()
	r, ok := modelRow(hermesImagesMan(), localModel)
	if !ok {
		t.Fatal("no model row for hermes")
	}
	return r.Options[r.Selected]
}

func TestModelRowOrderIsAPIKeysQwenGlimmer(t *testing.T) {
	t.Parallel()
	r, ok := modelRow(hermesImagesMan(), "")
	if !ok {
		t.Fatal("no model row for a def with local-model images")
	}
	if want := []string{modelAPIKeys, "Qwen 3.8 27B", "Muse Glimmer 30B"}; !slices.Equal(r.Options, want) {
		t.Errorf("options = %v, want %v", r.Options, want)
	}
	if r.Multi || r.Options[r.Selected] != modelAPIKeys {
		t.Errorf("want a single-select row defaulting to API keys, got multi=%v selected=%q", r.Multi, r.Options[r.Selected])
	}
}

func TestModelRowRecognisesAManualLocalModel(t *testing.T) {
	t.Parallel()
	for _, tag := range []string{"qwen3.8:latest", "qwen3.8:27b-mlx", "qwen3.8"} {
		if got := selected(t, tag); got != "Qwen 3.8 27B" {
			t.Errorf("--local-model %s selected %q, want Qwen 3.8 27B", tag, got)
		}
	}
	if got := selected(t, "muse-glimmer:30b-mlx"); got != "Muse Glimmer 30B" {
		t.Errorf("--local-model muse-glimmer:30b-mlx selected %q", got)
	}
	if got := modelLabelFor("qwen3.8:latest"); got != "Qwen 3.8 27B" {
		t.Errorf("an untouched row must map back to the same option, so the flag's exact tag is kept; got %q", got)
	}
	r, _ := modelRow(hermesImagesMan(), "gemma4:12b")
	if got := r.Options[r.Selected]; got != "gemma4:12b" || r.Help["gemma4:12b"] == "" {
		t.Errorf("an unlisted --local-model must appear as its own selected option with help, got %q in %v", got, r.Options)
	}
}

func TestModelRowOptionIsExactlyTheLocalModelFlag(t *testing.T) {
	t.Parallel()
	for opt, want := range map[string]string{
		modelAPIKeys:       "",
		"Qwen 3.8 27B":     localModels[0].tag(),
		"Muse Glimmer 30B": localModels[1].tag(),
		"gemma4:12b":       "gemma4:12b",
	} {
		if got := localModelFor(opt); got != want {
			t.Errorf("%q → --local-model %q, want %q", opt, got, want)
		}
	}
}

func TestModelRowListsOnlyImagesTheDefShips(t *testing.T) {
	t.Parallel()
	m := manifest.Manifest{Name: "hermes", Images: map[string]string{"hermes": "a", "hermes-qwen3.8": "b"}}
	r, _ := modelRow(m, "")
	if want := []string{modelAPIKeys, "Qwen 3.8 27B"}; !slices.Equal(r.Options, want) {
		t.Errorf("options = %v, want %v", r.Options, want)
	}
}

func TestNoModelRowWithoutLocalModelImages(t *testing.T) {
	t.Parallel()
	for _, m := range []manifest.Manifest{
		{Name: "cecli", Images: map[string]string{"cecli": "proveo/cecli:latest"}},
		{Name: "opencode", Images: map[string]string{"opencode": "o", "opencode-browser": "ob"}},
	} {
		if _, ok := modelRow(m, "qwen3.8:latest"); ok {
			t.Errorf("%s got a model row", m.Name)
		}
		if got := rememberedLocalModel(m, "qwen3.8:latest"); got != "" {
			t.Errorf("%s must not remember a --local-model it has no row for, got %q", m.Name, got)
		}
	}
}

func TestLocalModelIsSeededFromTheCache(t *testing.T) {
	t.Parallel()
	none := func(string) string { return "" }
	var p Params
	p.seedFromCache(agentsettings.Choice{LocalModel: "qwen3.8:latest"}, none, false)
	if p.LocalModel != "qwen3.8:latest" {
		t.Errorf("LocalModel = %q, want the cached qwen3.8:latest", p.LocalModel)
	}

	var legacy Params
	legacy.seedFromCache(agentsettings.Choice{ModelVariant: "hermes-muse-glimmer"}, none, false)
	if want := localModels[1].tag(); legacy.LocalModel != want {
		t.Errorf("legacy modelVariant → %q, want %s", legacy.LocalModel, want)
	}

	flag := Params{LocalModel: "gemma4:12b"}
	flag.seedFromCache(agentsettings.Choice{LocalModel: "qwen3.8:latest"}, none, false)
	if flag.LocalModel != "gemma4:12b" {
		t.Errorf("--local-model must win over the cache, got %q", flag.LocalModel)
	}
}

func TestLocalModelTagFollowsTheHostPlatform(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ platform, qwen, glimmer string }{
		{"darwin/arm64", "qwen3.8:27b-mlx", "muse-glimmer:30b-mlx"},
		{"linux/amd64", "qwen3.8:latest", "muse-glimmer:latest"},
		{"darwin/amd64", "qwen3.8:latest", "muse-glimmer:latest"},
		{"linux/arm64", "qwen3.8:latest", "muse-glimmer:latest"},
	} {
		if got := localModels[0].tagFor(c.platform); got != c.qwen {
			t.Errorf("Qwen on %s = %q, want %q", c.platform, got, c.qwen)
		}
		if got := localModels[1].tagFor(c.platform); got != c.glimmer {
			t.Errorf("Glimmer on %s = %q, want %q", c.platform, got, c.glimmer)
		}
	}
}
