// SPEC: _spec/internal/choiceui/wireframe.puml, _spec/defs/hermes/hermes-paradigm.puml
package run

import (
	"cmp"
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
	name, repo, short string            // short is the name the topology box prints
	tags              map[string]string // "goos/goarch" → Ollama tag; "" = every other host
	builds            map[string]string // "goos/goarch" → label suffix naming that build; "" = every other host
}

// localModels are the row's local choices in order.
var localModels = []localModel{
	{"Qwen 3.8 27B", "qwen3.8", "qwen3.8",
		map[string]string{"darwin/arm64": "qwen3.8:27b-mlx", "": "qwen3.8:latest"},
		map[string]string{"darwin/arm64": "MLX", "": "GGUF"}},
	{"Muse Glimmer 30B", "muse-glimmer", "glimmer",
		map[string]string{"darwin/arm64": "muse-glimmer:30b-mlx", "": "muse-glimmer:latest"},
		map[string]string{"darwin/arm64": "MLX", "": "GGUF"}},
}

func (m localModel) labelFor(string) string { return m.name }

func (m localModel) buildFor(platform string) string {
	if b, ok := m.builds[platform]; ok {
		return b
	}
	return m.builds[""]
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
		h[m.label()] = modelHint(m, "same as --local-model "+m.tag()+" on "+ollamaPlatform()+": the host's Ollama serves it, no key")
		if m.owns(localModel) {
			claimed = true
			if localModel != m.tag() {
				h[m.label()] = modelHint(m, "keeps your --local-model "+localModel+" (the row picks "+m.tag()+" on "+ollamaPlatform()+")")
			}
		}
	}
	if localModel != "" && !claimed {
		h[localModel] = "from --local-model: the host's Ollama serves it, no key"
	}
	return h
}

func modelHint(m localModel, rest string) string {
	if b := m.buildFor(ollamaPlatform()); b != "" {
		return b + ", " + rest
	}
	return rest
}

// noLocalModel names the harnesses whose inference is vendor-pinned.
var noLocalModel = map[string]bool{"cursor": true}

// acceptsLocalModel reports whether target can run on --local-model.
func acceptsLocalModel(target string) bool { return !noLocalModel[target] }

// modelRiskRank orders the model row riskier to safer, matching the axis legend.
func modelRiskRank(opt string) int {
	switch {
	case len(localModels) > 0 && opt == localModels[0].label():
		return 0
	case opt == modelAPIKeys:
		return 1
	case opt == credentials.AuthSubscription:
		return 2
	case len(localModels) > 1 && opt == localModels[1].label():
		return 3
	}
	return 4
}

func sortModelOptions(opts []string) {
	slices.SortStableFunc(opts, func(a, b string) int {
		return cmp.Compare(modelRiskRank(a), modelRiskRank(b))
	})
}

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
	sortModelOptions(opts)
	r := axisRow(rowModel, opts, nil, preselect)
	r.Help = modelHelp(localModel)
	pinModelCols(&r)
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
		sortModelRow(&r)
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
	sortModelRow(&r)
	return r, true
}

// sortModelRow orders options riskier to safer, carrying each option's gate with it.
func sortModelRow(r *choiceui.Row) {
	chosen := ""
	if r.Selected >= 0 && r.Selected < len(r.Options) {
		chosen = r.Options[r.Selected]
	}
	type item struct {
		opt string
		off bool
	}
	items := make([]item, len(r.Options))
	for i, opt := range r.Options {
		items[i] = item{opt, i < len(r.Off) && r.Off[i]}
	}
	slices.SortStableFunc(items, func(a, b item) int {
		return cmp.Compare(modelRiskRank(a.opt), modelRiskRank(b.opt))
	})
	for i, it := range items {
		r.Options[i] = it.opt
		if i < len(r.Off) {
			r.Off[i] = it.off
		}
	}
	if i := slices.Index(r.Options, chosen); i >= 0 {
		r.Selected = i
	}
	pinModelCols(r)
}

func pinModelCols(r *choiceui.Row) {
	r.Cols = make([]int, len(r.Options))
	for i, opt := range r.Options {
		r.Cols[i] = modelRiskRank(opt)
	}
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

// modelShort is what the topology box prints for a model-row answer; "" for API keys.
func modelShort(option string) string {
	switch option {
	case "", modelAPIKeys, credentials.AuthUsage:
		return ""
	case credentials.AuthSubscription:
		return "plan"
	}
	for _, m := range localModels {
		if m.label() == option {
			return m.short
		}
	}
	return option
}
