// SPEC: _spec/internal/choiceui/wireframe.puml, _spec/defs/hermes/hermes-paradigm.puml
package run

import (
	"cmp"
	"fmt"
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
	modelPlan    = "plan"
	modelTier24  = 24
	modelTier96  = 96
)

func modelTier(capacity uint64) int {
	switch {
	case capacity >= modelTier96<<30:
		return modelTier96
	case capacity >= modelTier24<<30:
		return modelTier24
	default:
		return 0
	}
}

// localModel is one row choice: repo claims any of its tags, tags and builds pick this host's build.
type localModel struct {
	name, repo, short, size string // short is the topology box; size is the hint, not the cell
	tags                    map[string]string
	builds                  map[string]string
	tier, column            int
}

// localModels are the row's local choices in order.
var localModels = []localModel{
	{"Qwen 3.8", "qwen3.8", "qwen3.8", "27B",
		map[string]string{"darwin/arm64": "qwen3.8:27b-mlx", "": "qwen3.8:latest"},
		map[string]string{"darwin/arm64": "MLX", "": "GGUF"}, modelTier24, 3},
	{"Glimmer", "muse-glimmer", "glimmer", "30B",
		map[string]string{"darwin/arm64": "muse-glimmer:30b-mlx", "": "muse-glimmer:latest"},
		map[string]string{"darwin/arm64": "MLX", "": "GGUF"}, modelTier24, 2},
	{"Nemotron", "nemotron-3.5-super", "nemotron", "120B-A12B",
		map[string]string{"": "nemotron-3.5-super:120b-a12b-q4_K_M"},
		map[string]string{"": "GGUF"}, modelTier96, 2},
	{"Flash Next", "qwen3.8-flash-next", "flash-next", "125B-A6B",
		map[string]string{"darwin/arm64": "qwen3.8-flash-next:125b-mlx", "": "qwen3.8-flash-next:125b-a6b-q4_K_M"},
		map[string]string{"darwin/arm64": "MLX", "": "GGUF"}, modelTier96, 3},
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

func (m localModel) hintLead() string {
	switch m.repo {
	case "muse-glimmer":
		return "Muse Glimmer " + m.size
	case "nemotron-3.5-super":
		return "Nemotron 3.5 Super " + m.size + " (~90 GB weights)"
	case "qwen3.8-flash-next":
		if ollamaPlatform() == "darwin/arm64" {
			return "Qwen3.8-Flash-Next " + m.size + " (~105 GB weights, 128 GB unified recommended)"
		}
		return "Qwen3.8-Flash-Next " + m.size + " (~120 GB weights, 128 GB VRAM recommended)"
	}
	if m.size != "" {
		return m.size
	}
	return m.name
}

func modelHint(m localModel, rest string) string {
	lead := m.hintLead()
	if b := m.buildFor(ollamaPlatform()); b != "" {
		lead += ", " + b
	}
	return lead + ", " + rest
}

// noLocalModel names the harnesses whose inference is vendor-pinned.
var noLocalModel = map[string]bool{"cursor": true}

// acceptsLocalModel reports whether target can run on --local-model.
func acceptsLocalModel(target string) bool { return !noLocalModel[target] }

// modelFlags maps a harness to the native flag its agent takes for a remote provider/model.
var modelFlags = map[string]string{"opencode": "-m", "codex": "--model", "claudecode": "--model"}

// acceptsRemoteModel reports the passthrough flag a remote model spec takes on target.
func acceptsRemoteModel(target string) (string, bool) {
	flag, ok := modelFlags[target]
	return flag, ok
}

// applyRemoteModel routes --model onto the harness's native flag through the agent passthrough.
func applyRemoteModel(p *Params) error {
	if p.Model == "" {
		return nil
	}
	flag, ok := acceptsRemoteModel(p.Target)
	if !ok {
		return fmt.Errorf("%s has no --model path (inference is vendor-pinned or not wired); unset it or use another harness", p.Target)
	}
	end := slices.Index(p.Extra, "--")
	if end < 0 {
		end = len(p.Extra)
	}
	p.Extra = slices.Insert(p.Extra, end, flag, p.Model)
	return nil
}

// modelColumn maps options to the shared grid.
func modelColumn(opt string, tier int) int {
	switch opt {
	case modelAPIKeys:
		return 0
	case credentials.AuthSubscription, modelPlan:
		return 1
	}
	for _, m := range localModels {
		if opt == m.label() && m.tier == tier {
			return m.column
		}
	}
	if tier == 0 {
		return 2
	}
	return 4
}

// modelRow is the single-select model row, drawn for every harness that accepts --local-model.
func modelRow(man manifest.Manifest, localModel string, tier int) (choiceui.Row, bool) {
	if !acceptsLocalModel(man.Name) {
		return choiceui.Row{}, false
	}
	opts := []string{modelAPIKeys}
	preselect := modelAPIKeys
	for _, m := range localModels {
		if m.tier != tier && !m.owns(localModel) {
			continue
		}
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
	sortModelRow(&r, tier)
	return r, true
}

func hasModelRow(man manifest.Manifest) bool {
	return acceptsLocalModel(man.Name)
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
		if !m.owns(tag) {
			continue
		}
		if m.tier == modelTier24 || tag == m.tagFor("") || tag == m.tagFor("darwin/arm64") {
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
func sourceRow(man manifest.Manifest, auth choiceui.Row, hasAuth bool, localModel string, tier int) (choiceui.Row, bool) {
	local, hasLocal := modelRow(man, localModel, tier)
	if !hasAuth {
		return local, hasLocal
	}
	r := auth
	r.Label = rowModel
	renameOption(&r, credentials.AuthUsage, modelAPIKeys)
	renameOption(&r, credentials.AuthSubscription, modelPlan)
	if !hasLocal {
		sortModelRow(&r, tier)
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
	sortModelRow(&r, tier)
	return r, true
}

// sortModelRow orders options by column, carrying each option's gate with it.
func sortModelRow(r *choiceui.Row, tier int) {
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
		return cmp.Compare(modelColumn(a.opt, tier), modelColumn(b.opt, tier))
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
	pinModelCols(r, tier)
}

func pinModelCols(r *choiceui.Row, tier int) {
	r.Cols = make([]int, len(r.Options))
	for i, opt := range r.Options {
		r.Cols[i] = modelColumn(opt, tier)
	}
	if len(r.Cols) == 0 {
		return
	}
	first, last := r.Cols[0], r.Cols[len(r.Cols)-1]
	if first < 2 {
		r.ColumnGroups = append(r.ColumnGroups, choiceui.ColumnGroup{Label: "cloud", First: first, Last: min(last, 1)})
	}
	if last >= 2 {
		label := "local"
		switch {
		case tier > 0 && ollamaPlatform() == "darwin/arm64":
			label = fmt.Sprintf("local (%d GB unified)", tier)
		case tier == modelTier96:
			label = "local (96 GB VRAM tier)"
		case tier == modelTier24:
			label = "local (needs 24 GB VRAM)"
		}
		r.ColumnGroups = append(r.ColumnGroups, choiceui.ColumnGroup{Label: label, First: max(first, 2), Last: last})
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
	return v != modelAPIKeys && v != modelPlan && !credentials.IsAuthSentinel(v)
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
	case v == modelPlan:
		p.AuthVar = credentials.AuthSubscription
	case v != modelAPIKeys:
		p.AuthVar = v
	}
}

// localBesideHost is the name drawn under the host when option is a local model.
func localBesideHost(option string) (string, bool) {
	if option == "" || !isLocalOption(option) {
		return "", false
	}
	for _, m := range localModels {
		if m.label() == option {
			return m.label(), true
		}
	}
	return option, true
}

// modelShort is what the topology box prints for a model-row answer; "" for API keys.
func modelShort(option string) string {
	switch option {
	case "", modelAPIKeys, credentials.AuthUsage:
		return ""
	case credentials.AuthSubscription, modelPlan:
		return "plan"
	}
	for _, m := range localModels {
		if m.label() == option {
			return m.short
		}
	}
	return option
}
