// SPEC: _spec/defs/hermes/hermes-persistence.puml (EVERY SESSION)
package sandbox

import (
	"path/filepath"
	"strings"
)

// agentHomes names, per def, the variable that holds the agent's home and its
// durable directory under the proveo state home.
var agentHomes = map[string]struct{ Var, Sub string }{
	"hermes": {"HERMES_HOME", filepath.Join("hermes", "data")},
}

// durableAgentHome points every session in the sandbox — the agent, sbx exec,
// SSH — at the def's durable home, when the run mounts a state home.
func durableAgentHome(target string, env []string) []string {
	h, ok := agentHomes[target]
	state := StateHome(env)
	if !ok || state == "" {
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if strings.HasPrefix(e, h.Var+"=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, h.Var+"="+filepath.Join(state, h.Sub))
}
