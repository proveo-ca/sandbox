// SPEC: _spec/internal/sbx/host-inference.puml (BEST HOST MODEL)
package run

import (
	"os"

	"github.com/proveo-ca/proveo/internal/envflag"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

var fittingHostModels = sbx.FittingHostModels

// autoLocalModel picks the host's best-fitting listed model for a run with no
// model, no provider key and no subscription; "" leaves the run as it is.
func autoLocalModel(rs *Spec, p *Params, getenv func(string) string) string {
	if p.LocalModel != "" || p.PrintOnly || !acceptsLocalModel(p.Target) || rs.Man.Subscription ||
		len(rs.Creds.Detected) > 0 || envflag.Off(getenv(sbx.EnvLocalModelAuto)) {
		return ""
	}
	var tags []string
	for _, m := range localModels {
		tags = append(tags, m.tag())
	}
	fit := fittingHostModels(tags, getenv)
	if len(fit) == 0 {
		return ""
	}
	ui.Hostf("local model: no provider key — %s on the host's Ollama, the best fit for this host's GPU (%s=0 turns this off)",
		fit[0], sbx.EnvLocalModelAuto)
	return fit[0]
}

func applyAutoLocalModel(rs *Spec, p *Params) {
	if m := autoLocalModel(rs, p, os.Getenv); m != "" {
		p.LocalModel = m
	}
}
