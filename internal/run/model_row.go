// SPEC: _spec/internal/choiceui/wireframe.puml, _spec/defs/hermes/hermes-paradigm.puml
package run

import (
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/manifest"
)

const (
	rowModel     = "model"
	modelAPIKeys = "API keys"
)

// bakedModels labels each baked-model image a def may list, in row order.
var bakedModels = []struct{ image, label string }{
	{"hermes-muse-glimmer", "Muse Glimmer 30B"},
	{"hermes-qwen3.8", "Qwen 3.8 27B"},
}

var modelHelp = map[string]string{
	modelAPIKeys:       "the model comes from your provider keys, brokered like any usage key",
	"Muse Glimmer 30B": "runs muse-glimmer:30b-q4_K_M baked into the image; no key, no network",
	"Qwen 3.8 27B":     "runs qwen3.8:27b-q4_K_M baked into the image; no key, no network",
}

// modelRow is the single-select model row, drawn only for a def that lists a baked-model image.
func modelRow(man manifest.Manifest, chosen string) (choiceui.Row, bool) {
	opts := []string{modelAPIKeys}
	preselect := modelAPIKeys
	for _, b := range bakedModels {
		if _, ok := man.Images[b.image]; !ok {
			continue
		}
		opts = append(opts, b.label)
		if b.image == chosen {
			preselect = b.label
		}
	}
	if len(opts) < 2 {
		return choiceui.Row{}, false
	}
	r := axisRow(rowModel, opts, nil, preselect)
	r.Help = modelHelp
	return r, true
}

// modelVariantFor maps a row option to its image key; "" means API keys.
func modelVariantFor(option string) string {
	for _, b := range bakedModels {
		if b.label == option {
			return b.image
		}
	}
	return ""
}

// modelVariantRef is the published image for a chosen baked model; "" leaves the image alone.
func modelVariantRef(man manifest.Manifest, variant string) string {
	if variant == "" {
		return ""
	}
	return man.Images[variant]
}
