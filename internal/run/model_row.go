// SPEC: _spec/internal/choiceui/wireframe.puml, _spec/defs/hermes/hermes-paradigm.puml
package run

import (
	"runtime"
	"strings"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/manifest"
)

const (
	rowModel     = "model"
	modelAPIKeys = "API keys"
)

// localModel is one row choice: image gates it, repo claims any of its tags, tags picks this host's build.
type localModel struct {
	image, label, repo string
	tags               map[string]string // "goos/goarch" → Ollama tag; "" = every other host
}

// localModels are the row's local choices in order.
var localModels = []localModel{
	{"hermes-qwen3.8", "Qwen 3.8 27B", "qwen3.8", map[string]string{
		"darwin/arm64": "qwen3.8:27b-mlx", "": "qwen3.8:latest"}},
	{"hermes-muse-glimmer", "Muse Glimmer 30B", "muse-glimmer", map[string]string{
		"darwin/arm64": "muse-glimmer:30b-mlx", "": "muse-glimmer:latest"}},
}

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
		h[m.label] = "same as --local-model " + m.tag() + " on " + ollamaPlatform() + ": the host's Ollama serves it, no key"
		if m.owns(localModel) {
			claimed = true
			if localModel != m.tag() {
				h[m.label] = "keeps your --local-model " + localModel + " (the row picks " + m.tag() + " on " + ollamaPlatform() + ")"
			}
		}
	}
	if localModel != "" && !claimed {
		h[localModel] = "from --local-model: the host's Ollama serves it, no key"
	}
	return h
}

// modelRow is the single-select model row, drawn only for a def that lists a local-model image.
func modelRow(man manifest.Manifest, localModel string) (choiceui.Row, bool) {
	opts := []string{modelAPIKeys}
	preselect := modelAPIKeys
	for _, m := range localModels {
		if _, ok := man.Images[m.image]; !ok {
			continue
		}
		opts = append(opts, m.label)
		if m.owns(localModel) {
			preselect = m.label
		}
	}
	if len(opts) < 2 {
		return choiceui.Row{}, false
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
		if m.label == option {
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
			return m.label
		}
	}
	return localModel
}

// legacyVariantTag maps a cached modelVariant image key to its --local-model value.
func legacyVariantTag(variant string) string {
	for _, m := range localModels {
		if m.image == variant {
			return m.tag()
		}
	}
	return ""
}
