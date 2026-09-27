// SPEC: _spec/internal/choiceui/wireframe.puml, _spec/defs/hermes/hermes-paradigm.puml
package run

import (
	"runtime"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/manifest"
)

const (
	rowModel     = "model"
	modelAPIKeys = "API keys"
)

// localModel is one row choice: repo claims any of its tags, tags and builds pick this host's build.
type localModel struct {
	name, repo string
	tags       map[string]string // "goos/goarch" → Ollama tag; "" = every other host
	builds     map[string]string // "goos/goarch" → label suffix naming that build; "" = every other host
}

// localModels are the row's local choices in order.
var localModels = []localModel{
	{"Qwen 3.8 27B", "qwen3.8",
		map[string]string{"darwin/arm64": "qwen3.8:27b-mlx", "": "qwen3.8:latest"},
		map[string]string{"darwin/arm64": "MLX", "": "GGUF"}},
	{"Muse Glimmer 30B", "muse-glimmer",
		map[string]string{"darwin/arm64": "muse-glimmer:30b-mlx", "": "muse-glimmer:latest"},
		map[string]string{"darwin/arm64": "MLX", "": "GGUF"}},
}

// labelFor is the option text on platform.
func (m localModel) labelFor(platform string) string {
	b, ok := m.builds[platform]
	if !ok {
		b = m.builds[""]
	}
	if b == "" {
		return m.name
	}
	return m.name + " " + b
}

func (m localModel) label() string { return m.labelFor(ollamaPlatform()) }

func ollamaPlatform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// tagFor is the Ollama tag for platform.
func (m localModel) tagFor(platform string) string {
	if t, ok := m.tags[platform]; ok {
		return t
	}
	return m.tags[""]
}

func (m localModel) tag() string { return m.tagFor(ollamaPlatform()) }

// owns reports whether tag is any build of this model.
func (m localModel) owns(tag string) bool { return tag == m.repo || strings.HasPrefix(tag, m.repo+":") }

func modelHelp(localModel string) map[string]string {
	h := map[string]string{
		modelAPIKeys: "no local model: the agent uses an external LLM provider through your keys, brokered like any usage key",
	}
	claimed := false
	for _, m := range localModels {
		h[m.label()] = "same as --local-model " + m.tag() + " on " + ollamaPlatform() + ": the host's Ollama serves it, no key"
		if m.owns(localModel) {
			claimed = true
			if localModel != m.tag() {
				h[m.label()] = "keeps your --local-model " + localModel + " (the row picks " + m.tag() + " on " + ollamaPlatform() + ")"
			}
		}
	}
	if localModel != "" && !claimed {
		h[localModel] = "from --local-model: the host's Ollama serves it, no key"
	}
	return h
}

// noLocalModel names the harnesses whose inference is vendor-pinned.
var noLocalModel = map[string]bool{"cursor": true}

// acceptsLocalModel reports whether target can run on --local-model.
func acceptsLocalModel(target string) bool { return !noLocalModel[target] }

// modelRow is the single-select model row, drawn for every harness that accepts --local-model.
func modelRow(man manifest.Manifest, localModel string) (choiceui.Row, bool) {
	if !acceptsLocalModel(man.Name) {
		return choiceui.Row{}, false
	}
	opts := []string{modelAPIKeys}
	preselect := modelAPIKeys
	for _, m := range localModels {
		opts = append(opts, m.label())
		if m.owns(localModel) {
			preselect = m.label()
		}
	}
	if localModel != "" && preselect == modelAPIKeys {
		opts = append(opts, localModel)
		preselect = localModel
	}
	r := axisRow(rowModel, opts, nil, preselect)
	r.Help = modelHelp(localModel)
	return r, true
}

func hasModelRow(man manifest.Manifest) bool {
	_, ok := modelRow(man, "")
	return ok
}

// localModelFor is the --local-model value a row option stands for; "" means API keys.
func localModelFor(option string) string {
	if option == modelAPIKeys {
		return ""
	}
	for _, m := range localModels {
		if m.label() == option {
			return m.tag()
		}
	}
	return option
}

// modelLabelFor is the row option a --local-model value preselects.
func modelLabelFor(localModel string) string {
	if localModel == "" {
		return modelAPIKeys
	}
	for _, m := range localModels {
		if m.owns(localModel) {
			return m.label()
		}
	}
	return localModel
}

// hostTagFor maps a remembered tag of a listed model to this host's build; other tags pass through.
func hostTagFor(tag string) string {
	for _, m := range localModels {
		if m.owns(tag) {
			return m.tag()
		}
	}
	return tag
}

// legacyVariantTag maps a cached modelVariant image key to its --local-model value.
func legacyVariantTag(variant string) string {
	for _, m := range localModels {
		if variant == "hermes-"+m.repo {
			return m.tag()
		}
	}
	return ""
}

// sourceRow is the one row that picks where the model comes from: provider keys, the harness's plan, or a local model.
func sourceRow(man manifest.Manifest, auth choiceui.Row, hasAuth bool, localModel string) (choiceui.Row, bool) {
	local, hasLocal := modelRow(man, localModel)
	if !hasAuth {
		return local, hasLocal
	}
	r := auth
	r.Label = rowModel
	renameOption(&r, credentials.AuthUsage, modelAPIKeys)
	if !hasLocal {
		return r, true
	}
	if r.Help == nil {
		r.Help = map[string]string{}
	}
	for len(r.Off) < len(r.Options) {
		r.Off = append(r.Off, false)
	}
	for _, opt := range local.Options {
		if opt == modelAPIKeys {
			continue
		}
		r.Options = append(r.Options, opt)
		r.Off = append(r.Off, false)
		r.Help[opt] = local.Help[opt]
	}
	if localModel != "" {
		if i := slices.Index(r.Options, modelLabelFor(localModel)); i >= 0 {
			r.Selected = i
		}
	}
	return r, true
}

func renameOption(r *choiceui.Row, from, to string) {
	i := slices.Index(r.Options, from)
	if i < 0 {
		return
	}
	r.Options = slices.Clone(r.Options)
	r.Options[i] = to
	if h, ok := r.Help[from]; ok {
		r.Help[to] = h
		delete(r.Help, from)
	}
	if w, ok := r.OffWhy[from]; ok {
		r.OffWhy[to] = w
		delete(r.OffWhy, from)
	}
	r.Reason = strings.ReplaceAll(r.Reason, from+": ", to+": ")
}

// isLocalOption reports whether a source-row option names a local model.
func isLocalOption(v string) bool {
	return v != modelAPIKeys && !credentials.IsAuthSentinel(v)
}

// applySource records a source-row answer as --local-model and, on a harness with a plan, the auth answer.
func (p *Params) applySource(v string, hasAuth bool) {
	if isLocalOption(v) {
		if v != modelLabelFor(p.LocalModel) {
			p.LocalModel = localModelFor(v)
		}
		if hasAuth {
			p.AuthVar = credentials.AuthLocal
		}
		return
	}
	p.LocalModel = ""
	switch {
	case v == modelAPIKeys && hasAuth:
		p.AuthVar = credentials.AuthUsage
	case v != modelAPIKeys:
		p.AuthVar = v
	}
}
