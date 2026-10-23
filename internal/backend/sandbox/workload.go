// SPEC: _spec/internal/sbx/sbx-kit-v3.puml
package sandbox

import (
	"gopkg.in/yaml.v3"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// Workload is a run's v3 kit: the descriptor and the recipe beside it.
type Workload struct {
	Descriptor sbx.WorkloadV3
	Recipe     sbx.RecipeV3
}

// Allow is the workload's network-policy@1 runtime allow list.
func (w Workload) Allow() []string {
	var out []string
	for _, c := range w.capabilities(sbx.CapNetworkPolicy) {
		var p sbx.NetworkPolicyV3
		if decode(c.Config, &p) == nil {
			out = append(out, p.Runtime.Allow...)
		}
	}
	return out
}

// Credentials are every credential@1 the workload declares, copied or proveo's own.
func (w Workload) Credentials() []sbx.CredentialV3 {
	var out []sbx.CredentialV3
	for _, c := range w.capabilities(sbx.CapCredential) {
		var cred sbx.CredentialV3
		if decode(c.Config, &cred) == nil {
			out = append(out, cred)
		}
	}
	return out
}

// Hook is one lifecycle hook as the descriptor declares it, its argv normalised.
type Hook struct {
	Phase       string
	Argv        []string
	Script      string
	User        string
	Background  bool
	Env         []string
	Description string
}

// Hooks are the workload's install then startup hooks, in declared order.
func (w Workload) Hooks() []Hook {
	var out []Hook
	for _, c := range w.capabilities(sbx.CapLifecycle) {
		var cfg map[string][]struct {
			Command     any      `yaml:"command"`
			User        string   `yaml:"user"`
			Background  bool     `yaml:"background"`
			Env         []string `yaml:"env"`
			Description string   `yaml:"description"`
		}
		if decode(c.Config, &cfg) != nil {
			continue
		}
		for _, phase := range []string{"install", "startup"} {
			for _, h := range cfg[phase] {
				hook := Hook{Phase: phase, User: h.User, Background: h.Background, Env: h.Env, Description: h.Description}
				switch cmd := h.Command.(type) {
				case string:
					hook.Script = cmd
				case []any:
					for _, a := range cmd {
						s, _ := a.(string)
						hook.Argv = append(hook.Argv, s)
					}
				}
				out = append(out, hook)
			}
		}
	}
	return out
}

func (w Workload) capabilities(typ string) []sbx.CapabilityV3 {
	var out []sbx.CapabilityV3
	for _, c := range w.Descriptor.Capabilities {
		if c.Type == typ {
			out = append(out, c)
		}
	}
	return out
}

func decode(v, into any) error {
	b, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(b, into)
}
