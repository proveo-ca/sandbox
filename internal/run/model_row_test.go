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

func TestModelRowOffersAPIKeysAndEachBakedModel(t *testing.T) {
	t.Parallel()
	r, ok := modelRow(hermesImagesMan(), "")
	if !ok {
		t.Fatal("no model row for a def with baked-model images")
	}
	want := []string{modelAPIKeys, "Muse Glimmer 30B", "Qwen 3.8 27B"}
	if !slices.Equal(r.Options, want) {
		t.Errorf("options = %v, want %v", r.Options, want)
	}
	if r.Multi || r.Options[r.Selected] != modelAPIKeys {
		t.Errorf("want a single-select row defaulting to API keys, got multi=%v selected=%q", r.Multi, r.Options[r.Selected])
	}
}

func TestModelRowPreselectsTheRememberedVariant(t *testing.T) {
	t.Parallel()
	r, _ := modelRow(hermesImagesMan(), "hermes-qwen3.8")
	if got := r.Options[r.Selected]; got != "Qwen 3.8 27B" {
		t.Errorf("selected %q, want the remembered Qwen 3.8 27B", got)
	}
	r, _ = modelRow(hermesImagesMan(), "hermes-gone")
	if got := r.Options[r.Selected]; got != modelAPIKeys {
		t.Errorf("an unknown remembered variant selected %q, want API keys", got)
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

func TestNoModelRowWithoutBakedModels(t *testing.T) {
	t.Parallel()
	for _, m := range []manifest.Manifest{
		{Name: "cecli", Images: map[string]string{"cecli": "proveo/cecli:latest"}},
		{Name: "opencode", Images: map[string]string{"opencode": "o", "opencode-browser": "ob"}},
	} {
		if _, ok := modelRow(m, ""); ok {
			t.Errorf("%s got a model row", m.Name)
		}
	}
}

func TestModelChoiceSelectsTheVariantImage(t *testing.T) {
	t.Parallel()
	m := hermesImagesMan()
	for opt, wantRef := range map[string]string{
		modelAPIKeys:       "",
		"Muse Glimmer 30B": "proveo/hermes-muse-glimmer:latest",
		"Qwen 3.8 27B":     "proveo/hermes-qwen3.8:latest",
	} {
		if got := modelVariantRef(m, modelVariantFor(opt)); got != wantRef {
			t.Errorf("%q → image %q, want %q", opt, got, wantRef)
		}
	}
}

func TestModelVariantIsSeededFromTheCache(t *testing.T) {
	t.Parallel()
	var p Params
	p.seedFromCache(agentsettings.Choice{ModelVariant: "hermes-muse-glimmer"}, func(string) string { return "" }, false)
	if p.ModelVariant != "hermes-muse-glimmer" {
		t.Errorf("ModelVariant = %q, want the cached hermes-muse-glimmer", p.ModelVariant)
	}
}
