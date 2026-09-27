// SPEC: _spec/internal/choiceui/wireframe.puml, _spec/defs/hermes/hermes-paradigm.puml
package run

import (
	"slices"
	"testing"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/manifest"
)

var (
	qwen    = localModels[0]
	glimmer = localModels[1]
)

func hermesImagesMan() manifest.Manifest {
	return manifest.Manifest{Name: "hermes", Images: map[string]string{"hermes": "proveo/hermes:latest"}}
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
		t.Fatal("no model row for hermes")
	}
	if want := []string{modelAPIKeys, qwen.label(), glimmer.label()}; !slices.Equal(r.Options, want) {
		t.Errorf("options = %v, want %v", r.Options, want)
	}
	if r.Multi || r.Options[r.Selected] != modelAPIKeys {
		t.Errorf("want a single-select row defaulting to API keys, got multi=%v selected=%q", r.Multi, r.Options[r.Selected])
	}
}

func TestModelRowRecognisesAManualLocalModel(t *testing.T) {
	t.Parallel()
	for _, tag := range []string{"qwen3.8:latest", "qwen3.8:27b-mlx", "qwen3.8"} {
		if got := selected(t, tag); got != qwen.label() {
			t.Errorf("--local-model %s selected %q, want %q", tag, got, qwen.label())
		}
	}
	if got := selected(t, "muse-glimmer:30b-mlx"); got != glimmer.label() {
		t.Errorf("--local-model muse-glimmer:30b-mlx selected %q", got)
	}
	if got := modelLabelFor("qwen3.8:latest"); got != qwen.label() {
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
		modelAPIKeys:    "",
		qwen.label():    qwen.tag(),
		glimmer.label(): glimmer.tag(),
		"gemma4:12b":    "gemma4:12b",
	} {
		if got := localModelFor(opt); got != want {
			t.Errorf("%q → --local-model %q, want %q", opt, got, want)
		}
	}
}

func TestModelRowIsDrawnForEveryHarnessThatAcceptsLocalModel(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"hermes", "opencode", "cecli", "codex", "claudecode"} {
		if _, ok := modelRow(manifest.Manifest{Name: name}, ""); !ok {
			t.Errorf("%s accepts --local-model but got no model row", name)
		}
	}
	cursor := manifest.Manifest{Name: "cursor"}
	if _, ok := modelRow(cursor, ""); ok {
		t.Error("cursor's inference is vendor-pinned; it must get no model row")
	}
	if got := rememberedLocalModel(cursor, "qwen3.8:latest"); got != "" {
		t.Errorf("cursor must not remember a --local-model, got %q", got)
	}
	if acceptsLocalModel("cursor") || !acceptsLocalModel("hermes") {
		t.Error("the row and the --local-model refusal must share one predicate")
	}
}

func TestLocalModelIsSeededFromTheCache(t *testing.T) {
	t.Parallel()
	none := func(string) string { return "" }
	var p Params
	p.seedFromCache(agentsettings.Choice{LocalModel: "qwen3.8:latest"}, none, false)
	if p.LocalModel != qwen.tag() {
		t.Errorf("a remembered qwen3.8:latest = %q, want this host's build %q", p.LocalModel, qwen.tag())
	}

	var custom Params
	custom.seedFromCache(agentsettings.Choice{LocalModel: "gemma4:12b"}, none, false)
	if custom.LocalModel != "gemma4:12b" {
		t.Errorf("an unlisted remembered tag must pass through, got %q", custom.LocalModel)
	}

	var legacy Params
	legacy.seedFromCache(agentsettings.Choice{ModelVariant: "hermes-muse-glimmer"}, none, false)
	if want := glimmer.tag(); legacy.LocalModel != want {
		t.Errorf("legacy modelVariant → %q, want %s", legacy.LocalModel, want)
	}

	flag := Params{LocalModel: "gemma4:12b"}
	flag.seedFromCache(agentsettings.Choice{LocalModel: "qwen3.8:latest"}, none, false)
	if flag.LocalModel != "gemma4:12b" {
		t.Errorf("--local-model must win over the cache, got %q", flag.LocalModel)
	}
}

func TestLocalModelFollowsTheHostPlatform(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ platform, qwenTag, qwenLabel, glimmerTag, glimmerLabel string }{
		{"darwin/arm64", "qwen3.8:27b-mlx", "Qwen 3.8 27B MLX", "muse-glimmer:30b-mlx", "Muse Glimmer 30B MLX"},
		{"linux/amd64", "qwen3.8:latest", "Qwen 3.8 27B GGUF", "muse-glimmer:latest", "Muse Glimmer 30B GGUF"},
		{"darwin/amd64", "qwen3.8:latest", "Qwen 3.8 27B GGUF", "muse-glimmer:latest", "Muse Glimmer 30B GGUF"},
		{"linux/arm64", "qwen3.8:latest", "Qwen 3.8 27B GGUF", "muse-glimmer:latest", "Muse Glimmer 30B GGUF"},
	} {
		if got := qwen.tagFor(c.platform); got != c.qwenTag {
			t.Errorf("Qwen tag on %s = %q, want %q", c.platform, got, c.qwenTag)
		}
		if got := qwen.labelFor(c.platform); got != c.qwenLabel {
			t.Errorf("Qwen label on %s = %q, want %q", c.platform, got, c.qwenLabel)
		}
		if got := glimmer.tagFor(c.platform); got != c.glimmerTag {
			t.Errorf("Glimmer tag on %s = %q, want %q", c.platform, got, c.glimmerTag)
		}
		if got := glimmer.labelFor(c.platform); got != c.glimmerLabel {
			t.Errorf("Glimmer label on %s = %q, want %q", c.platform, got, c.glimmerLabel)
		}
	}
}

func sourceFor(t *testing.T, man manifest.Manifest, lookup map[string]string, localModel string) (choiceui.Row, bool) {
	t.Helper()
	auth, hasAuth := authRow(man, env(lookup), man.Name, "", "", "")
	return sourceRow(man, auth, hasAuth, localModel)
}

func TestSourceRowMergesThePlanAndTheLocalModels(t *testing.T) {
	t.Parallel()
	keys := map[string]string{"OPENCODE_API_KEY": "zen", "ANTHROPIC_API_KEY": "sk"}
	r, ok := sourceFor(t, opencodeMan(), keys, "")
	if !ok {
		t.Fatal("opencode got no source row")
	}
	want := []string{modelAPIKeys, credentials.AuthSubscription, qwen.label(), glimmer.label()}
	if r.Label != rowModel || !slices.Equal(r.Options, want) {
		t.Errorf("row %q = %v, want %q = %v", r.Label, r.Options, rowModel, want)
	}
	if len(r.Off) != len(r.Options) || r.Help[qwen.label()] == "" || r.Help[modelAPIKeys] == "" {
		t.Errorf("merged row lost its gates or help: off=%v help=%v", r.Off, r.Help)
	}
	if got := r.Options[r.Selected]; got != credentials.AuthSubscription {
		t.Errorf("with a plan available the row opens on %q, want subscription", got)
	}

	r, _ = sourceFor(t, opencodeMan(), keys, "qwen3.8:latest")
	if got := r.Options[r.Selected]; got != qwen.label() {
		t.Errorf("--local-model preselects %q, want %q", got, qwen.label())
	}
}

func TestSourceRowKeepsCursorOffLocalModels(t *testing.T) {
	t.Parallel()
	r, ok := sourceFor(t, cursorMan(), map[string]string{"CURSOR_API_KEY": "c"}, "")
	if !ok {
		t.Fatal("cursor lost its plan row")
	}
	for _, m := range localModels {
		if slices.Contains(r.Options, m.label()) {
			t.Errorf("cursor-agent cannot target a custom endpoint, yet the row offers %q: %v", m.label(), r.Options)
		}
	}
}

func TestSourceRowWithoutAPlanIsTheModelRow(t *testing.T) {
	t.Parallel()
	r, ok := sourceFor(t, hermesImagesMan(), nil, "")
	if want := []string{modelAPIKeys, qwen.label(), glimmer.label()}; !ok || !slices.Equal(r.Options, want) {
		t.Errorf("hermes row = %v (%v), want %v", r.Options, ok, want)
	}
}

func TestApplySourceSetsLocalModelAndAuthTogether(t *testing.T) {
	t.Parallel()
	p := Params{}
	p.applySource(qwen.label(), true)
	if p.LocalModel != qwen.tag() || p.AuthVar != credentials.AuthLocal {
		t.Errorf("local on a plan harness: model=%q auth=%q", p.LocalModel, p.AuthVar)
	}
	p.applySource(credentials.AuthSubscription, true)
	if p.LocalModel != "" || p.AuthVar != credentials.AuthSubscription {
		t.Errorf("subscription must clear the local model: model=%q auth=%q", p.LocalModel, p.AuthVar)
	}
	p.applySource(modelAPIKeys, true)
	if p.LocalModel != "" || p.AuthVar != credentials.AuthUsage {
		t.Errorf("API keys on a plan harness = usage credits: model=%q auth=%q", p.LocalModel, p.AuthVar)
	}
	q := Params{}
	q.applySource(modelAPIKeys, false)
	if q.LocalModel != "" || q.AuthVar != "" {
		t.Errorf("API keys on hermes sets nothing: model=%q auth=%q", q.LocalModel, q.AuthVar)
	}
	keep := Params{LocalModel: "qwen3.8:latest"}
	keep.applySource(qwen.label(), false)
	if keep.LocalModel != "qwen3.8:latest" {
		t.Errorf("an untouched preselection must keep the flag's tag, got %q", keep.LocalModel)
	}
}
