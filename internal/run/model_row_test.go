// SPEC: _spec/internal/choiceui/wireframe.puml, _spec/defs/hermes/hermes-paradigm.puml
package run

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

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
	r, ok := modelRow(hermesImagesMan(), localModel, modelTier24)
	if !ok {
		t.Fatal("no model row for hermes")
	}
	return r.Options[r.Selected]
}

func TestModelRowOrderRunsRiskierToSafer(t *testing.T) {
	t.Parallel()
	r, ok := modelRow(hermesImagesMan(), "", modelTier24)
	if !ok {
		t.Fatal("no model row for hermes")
	}
	if want := []string{modelAPIKeys, glimmer.label(), qwen.label()}; !slices.Equal(r.Options, want) {
		t.Errorf("options = %v, want %v", r.Options, want)
	}
	if r.Multi || r.Options[r.Selected] != modelAPIKeys {
		t.Errorf("want a single-select row defaulting to API keys, got multi=%v selected=%q", r.Multi, r.Options[r.Selected])
	}
}

func TestModelRowSwitchesPresetsAtTheHostCapacityCutoffs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		memory uint64
		local  []string
	}{
		{0, nil},
		{(24 << 30) - 1, nil},
		{24 << 30, []string{"Glimmer", "Qwen 3.8"}},
		{(96 << 30) - 1, []string{"Glimmer", "Qwen 3.8"}},
		{96 << 30, []string{"Nemotron", "Flash Next"}},
		{128 << 30, []string{"Nemotron", "Flash Next"}},
	} {
		keys := env(map[string]string{"OPENCODE_API_KEY": "zen"})
		man := opencodeMan()
		auth, hasAuth := authRow(man, keys, man.Name, "", "", "")
		r, ok := sourceRow(man, auth, hasAuth, "", modelTier(tc.memory))
		want := append([]string{modelAPIKeys, modelPlan}, tc.local...)
		if !ok || !slices.Equal(r.Options, want) {
			t.Errorf("%d bytes: options = %v (%v), want %v", tc.memory, r.Options, ok, want)
		}
		wantCols := []int{0, 1, 2, 3}[:len(want)]
		if !slices.Equal(r.Cols, wantCols) {
			t.Errorf("%d bytes: cols = %v, want %v", tc.memory, r.Cols, wantCols)
		}
	}
}

func TestAnExplicitModelCanCrossTheTierBoundary(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		tag, option string
		tier        int
	}{
		{"qwen3.8:latest", "Qwen 3.8", modelTier96},
		{"nemotron-3.5-super:120b-a12b-nvfp4", "Nemotron", modelTier24},
		{"qwen3.8-flash-next:125b-a6b-q4_K_M", "Flash Next", 0},
	} {
		r, _ := modelRow(hermesImagesMan(), c.tag, c.tier)
		if got := r.Options[r.Selected]; got != c.option {
			t.Fatalf("tag %q selected %q, want %q", c.tag, got, c.option)
		}
		p := Params{LocalModel: c.tag}
		p.applySource(c.option, true)
		if p.LocalModel != c.tag || p.AuthVar != credentials.AuthLocal {
			t.Errorf("explicit tag became model %q and auth %q", p.LocalModel, p.AuthVar)
		}
	}
}

func TestLargePresetTagsUseLocalOllamaBuilds(t *testing.T) {
	t.Parallel()
	for _, m := range localModels {
		switch m.repo {
		case "nemotron-3.5-super":
			if got := m.tagFor("linux/amd64"); got != "nemotron-3.5-super:120b-a12b-q4_K_M" {
				t.Errorf("Nemotron Linux tag = %q", got)
			}
		case "qwen3.8-flash-next":
			if got := m.tagFor("linux/amd64"); got != "qwen3.8-flash-next:125b-a6b-q4_K_M" {
				t.Errorf("Flash-Next Linux tag = %q", got)
			}
			if got := m.tagFor("darwin/arm64"); got != "qwen3.8-flash-next:125b-mlx" {
				t.Errorf("Flash-Next Apple silicon tag = %q", got)
			}
		}
	}
	const custom = "nemotron-3.5-super:120b-a12b-nvfp4"
	if got := hostTagFor(custom); got != custom {
		t.Errorf("remembered custom Nemotron build = %q, want %q", got, custom)
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
	r, _ := modelRow(hermesImagesMan(), "gemma4:12b", modelTier24)
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
	for _, name := range []string{"hermes", "opencode", "codex", "claudecode"} {
		if _, ok := modelRow(manifest.Manifest{Name: name}, "", modelTier24); !ok {
			t.Errorf("%s accepts --local-model but got no model row", name)
		}
	}
	cursor := manifest.Manifest{Name: "cursor"}
	if _, ok := modelRow(cursor, "", modelTier24); ok {
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
	for _, c := range []struct{ platform, qwenTag, build, glimmerTag string }{
		{"darwin/arm64", "qwen3.8:27b-mlx", "MLX", "muse-glimmer:30b-mlx"},
		{"linux/amd64", "qwen3.8:latest", "GGUF", "muse-glimmer:latest"},
		{"darwin/amd64", "qwen3.8:latest", "GGUF", "muse-glimmer:latest"},
		{"linux/arm64", "qwen3.8:latest", "GGUF", "muse-glimmer:latest"},
	} {
		if got := qwen.tagFor(c.platform); got != c.qwenTag {
			t.Errorf("Qwen tag on %s = %q, want %q", c.platform, got, c.qwenTag)
		}
		if got := qwen.labelFor(c.platform); got != qwen.name {
			t.Errorf("Qwen label on %s = %q, want %q", c.platform, got, qwen.name)
		}
		if got := qwen.buildFor(c.platform); got != c.build {
			t.Errorf("Qwen build on %s = %q, want %q", c.platform, got, c.build)
		}
		if got := glimmer.tagFor(c.platform); got != c.glimmerTag {
			t.Errorf("Glimmer tag on %s = %q, want %q", c.platform, got, c.glimmerTag)
		}
		if got := glimmer.labelFor(c.platform); got != glimmer.name {
			t.Errorf("Glimmer label on %s = %q, want %q", c.platform, got, glimmer.name)
		}
		if got := glimmer.buildFor(c.platform); got != c.build {
			t.Errorf("Glimmer build on %s = %q, want %q", c.platform, got, c.build)
		}
	}
}

func TestModelBuildLivesInTheHint(t *testing.T) {
	t.Parallel()
	r, ok := modelRow(hermesImagesMan(), "", modelTier24)
	if !ok {
		t.Fatal("no model row")
	}
	for _, opt := range r.Options {
		if strings.Contains(opt, "GGUF") || strings.Contains(opt, "MLX") || strings.Contains(opt, "27B") || strings.Contains(opt, "30B") {
			t.Errorf("build or size leaked into the option %q", opt)
		}
		if n := utf8.RuneCountInString(opt); n > 10 {
			t.Errorf("option %q is %d runes, want at most 10", opt, n)
		}
	}
	if cols := []int{0, 2, 3}; !slices.Equal(r.Cols, cols) {
		t.Errorf("cols = %v, want %v (plan's column stays empty)", r.Cols, cols)
	}
	h := r.Help[qwen.label()]
	if !strings.Contains(h, "27B") || !strings.Contains(h, qwen.buildFor(ollamaPlatform())) {
		t.Errorf("hint = %q, want 27B and the %s build", h, qwen.buildFor(ollamaPlatform()))
	}
	g := r.Help[glimmer.label()]
	if !strings.Contains(g, "Muse Glimmer") || !strings.Contains(g, "30B") {
		t.Errorf("hint = %q, want Muse Glimmer and 30B", g)
	}
}

func sourceFor(t *testing.T, man manifest.Manifest, lookup map[string]string, localModel string) (choiceui.Row, bool) {
	t.Helper()
	auth, hasAuth := authRow(man, env(lookup), man.Name, "", "", "")
	return sourceRow(man, auth, hasAuth, localModel, modelTier24)
}

func TestSourceRowMergesThePlanAndTheLocalModels(t *testing.T) {
	t.Parallel()
	keys := map[string]string{"OPENCODE_API_KEY": "zen", "ANTHROPIC_API_KEY": "sk"}
	r, ok := sourceFor(t, opencodeMan(), keys, "")
	if !ok {
		t.Fatal("opencode got no source row")
	}
	want := []string{modelAPIKeys, modelPlan, glimmer.label(), qwen.label()}
	if r.Label != rowModel || !slices.Equal(r.Options, want) {
		t.Errorf("row %q = %v, want %q = %v", r.Label, r.Options, rowModel, want)
	}
	if !slices.Equal(r.Cols, []int{0, 1, 2, 3}) {
		t.Errorf("model cols = %v, want one option per column", r.Cols)
	}
	if len(r.Off) != len(r.Options) || r.Help[qwen.label()] == "" || r.Help[modelAPIKeys] == "" {
		t.Errorf("merged row lost its gates or help: off=%v help=%v", r.Off, r.Help)
	}
	if got := r.Options[r.Selected]; got != modelPlan {
		t.Errorf("with a plan available the row opens on %q, want %q", got, modelPlan)
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
	if want := []string{modelAPIKeys, glimmer.label(), qwen.label()}; !ok || !slices.Equal(r.Options, want) {
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
	p.applySource(modelPlan, true)
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

func TestApplyRemoteModelRoutesByHarness(t *testing.T) {
	t.Parallel()
	p := Params{Target: "opencode", Model: "opencode-go/glm-5.3"}
	if err := applyRemoteModel(&p); err != nil {
		t.Fatal(err)
	}
	if len(p.Extra) != 2 || p.Extra[0] != "-m" || p.Extra[1] != "opencode-go/glm-5.3" {
		t.Errorf("opencode extra = %v; want [-m opencode-go/glm-5.3]", p.Extra)
	}
	p = Params{Target: "codex", Model: "gpt-x"}
	if err := applyRemoteModel(&p); err != nil || p.Extra[0] != "--model" {
		t.Errorf("codex route = %v (%v); want --model", p.Extra, err)
	}
	if err := applyRemoteModel(&Params{Target: "cursor", Model: "any/model"}); err == nil {
		t.Error("cursor must refuse --model (vendor-pinned)")
	}
	if err := applyRemoteModel(&Params{Target: "hermes"}); err != nil {
		t.Errorf("no --model set: %v", err)
	}
	p = Params{Target: "opencode"}
	if err := applyRemoteModel(&p); err != nil || len(p.Extra) != 0 {
		t.Errorf("no --model must touch nothing: %v (%v)", p.Extra, err)
	}
}

func TestModelPinKeysNamesTheProviderKey(t *testing.T) {
	t.Parallel()
	msgs := modelPinKeys(&Params{Target: "opencode", Model: "opencode-go/glm-5"}, nil)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "needs OPENCODE_API_KEY (opencode), which is not set") {
		t.Errorf("pin keys = %v; want the OPENCODE_API_KEY ask", msgs)
	}
	if msgs := modelPinKeys(&Params{Target: "opencode", Model: "opencode-go/glm-5"}, []string{"opencode"}); len(msgs) != 0 {
		t.Errorf("a detected provider must stay silent: %v", msgs)
	}
	if msgs := modelPinKeys(&Params{Target: "opencode"}, nil); len(msgs) != 0 {
		t.Errorf("no pin must stay silent: %v", msgs)
	}
	if msgs := modelPinKeys(&Params{Target: "opencode", Model: "weird/unknown"}, nil); len(msgs) != 0 {
		t.Errorf("an unknown provider must stay silent: %v", msgs)
	}
}
