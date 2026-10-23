// SPEC: _spec/internal/sbx/sbx-kit-v3.puml
package sbx

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const KitSchemaVersionV3 = "3"

// KitSyntaxV3 must be the descriptor's first line.
const KitSyntaxV3 = "# syntax=docker/sandbox-kit:3"

const (
	CapSbx           = "com.docker.sandbox/sbx@1"
	CapLifecycle     = "com.docker.sandbox/lifecycle@1"
	CapCredential    = "com.docker.sandbox/credential@1"
	CapNetworkPolicy = "com.docker.sandbox/network-policy@1"
	CapPort          = "com.docker.sandbox/port@1"
	CapVolume        = "com.docker.sandbox/volume@1"
)

// WorkloadV3 is a v3 `kind: workload` descriptor; it carries no field the spec does not define.
type WorkloadV3 struct {
	SchemaVersion string         `yaml:"schemaVersion"`
	DisplayName   string         `yaml:"displayName,omitempty"`
	Description   string         `yaml:"description,omitempty"`
	Kind          string         `yaml:"kind"`
	Version       string         `yaml:"version"`
	Capabilities  []CapabilityV3 `yaml:"capabilities"`
}

type CapabilityV3 struct {
	Type     string `yaml:"type"`
	Optional bool   `yaml:"optional,omitempty"`
	Config   any    `yaml:"config,omitempty"`
}

// LifecycleV3 is lifecycle@1's config.
type LifecycleV3 struct {
	Install []HookV3 `yaml:"install,omitempty"`
	Startup []HookV3 `yaml:"startup,omitempty"`
}

// HookV3 is one lifecycle hook; Command is a string (run via `sh -c`) or an argv list.
type HookV3 struct {
	Command     any      `yaml:"command"`
	User        string   `yaml:"user,omitempty"`
	Background  bool     `yaml:"background,omitempty"`
	Env         []string `yaml:"env,omitempty"`
	Description string   `yaml:"description,omitempty"`
}

// NetworkPolicyV3 is network-policy@1's config.
type NetworkPolicyV3 struct {
	Runtime NetworkPhaseV3 `yaml:"runtime"`
}

type NetworkPhaseV3 struct {
	Allow []string `yaml:"allow,omitempty"`
	Deny  []string `yaml:"deny,omitempty"`
}

// PortV3 is port@1's config.
type PortV3 struct {
	Name      string `yaml:"name,omitempty"`
	Container int    `yaml:"container"`
	Transport string `yaml:"transport,omitempty"`
}

// RecipeV3 is the workload's <stem>.dockerfile.
type RecipeV3 struct {
	From       string
	Env        []string
	Entrypoint []string
	Cmd        []string
}

// LaunchArgs is the trailing command that adds flags to launch: sbx replaces everything after argv0, so launch[1:] leads.
func LaunchArgs(launch, flags []string) []string {
	if len(flags) == 0 || len(launch) == 0 {
		return nil
	}
	return append(append([]string{}, launch[1:]...), flags...)
}

// CredentialV3 is credential@1's config.
type CredentialV3 struct {
	Service string    `yaml:"service"`
	Phase   string    `yaml:"phase,omitempty"`
	APIKey  *APIKeyV3 `yaml:"apiKey,omitempty"`
}

type APIKeyV3 struct {
	Name         string         `yaml:"name"`
	ProxyManaged bool           `yaml:"proxyManaged,omitempty"`
	Inject       []CredInjectV3 `yaml:"inject,omitempty"`
}

type CredInjectV3 struct {
	Domain string `yaml:"domain"`
	Header string `yaml:"header"`
	Format string `yaml:"format,omitempty"`
}

// KitDirV3 is the workload directory for agent: its base name is the agent name sbx registers.
func KitDirV3(root, agent string) string { return filepath.Join(root, agent) }

// WriteWorkloadV3 writes <dir>/<agent>.yaml and <dir>/<agent>.dockerfile, where agent is dir's base name.
func WriteWorkloadV3(dir string, w WorkloadV3, r RecipeV3) (string, error) {
	agent := filepath.Base(dir)
	if err := checkAgentNameV3(agent); err != nil {
		return "", err
	}
	if r.From == "" {
		return "", fmt.Errorf("sbx v3 kit %s: the recipe names no base image", agent)
	}
	w.SchemaVersion, w.Kind = KitSchemaVersionV3, "workload"
	var buf bytes.Buffer
	buf.WriteString(KitSyntaxV3 + "\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(w); err != nil {
		return "", fmt.Errorf("sbx v3 kit encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("sbx v3 kit encode: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("sbx v3 kit dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, agent+".yaml"), buf.Bytes(), 0o600); err != nil {
		return "", fmt.Errorf("sbx v3 kit write: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, agent+".dockerfile"), []byte(r.render()), 0o600); err != nil {
		return "", fmt.Errorf("sbx v3 kit write: %w", err)
	}
	return dir, nil
}

func (r RecipeV3) render() string {
	var b strings.Builder
	b.WriteString("# syntax=docker/dockerfile:1\n")
	fmt.Fprintf(&b, "FROM %s\n", r.From)
	for _, kv := range r.Env {
		k, v, _ := strings.Cut(kv, "=")
		fmt.Fprintf(&b, "ENV %s=%q\n", k, v)
	}
	if len(r.Entrypoint) > 0 {
		fmt.Fprintf(&b, "ENTRYPOINT %s\nCMD %s\n", execForm(r.Entrypoint), execForm(r.Cmd))
	}
	return b.String()
}

func execForm(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = fmt.Sprintf("%q", a)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func checkAgentNameV3(agent string) error {
	switch {
	case agent == "spec":
		return fmt.Errorf("sbx v3 kit %q: a descriptor named spec.yaml is parsed as v2", agent)
	case slices.Contains(BuiltinAgents, agent):
		return fmt.Errorf("sbx v3 kit %q: the name of a built-in agent cannot register", agent)
	}
	return nil
}

// WithLifecycle appends hooks to the descriptor's one lifecycle@1 entry (a singleton), adding it when absent.
func WithLifecycle(caps []CapabilityV3, add LifecycleV3) ([]CapabilityV3, error) {
	extra, err := asMap(add)
	if err != nil {
		return nil, err
	}
	for i, c := range caps {
		if c.Type != CapLifecycle {
			continue
		}
		cfg, err := asMap(c.Config)
		if err != nil {
			return nil, err
		}
		for _, phase := range []string{"install", "startup"} {
			more, _ := extra[phase].([]any)
			if len(more) == 0 {
				continue
			}
			have, _ := cfg[phase].([]any)
			cfg[phase] = append(have, more...)
		}
		caps[i].Config = cfg
		return caps, nil
	}
	return append(caps, CapabilityV3{Type: CapLifecycle, Config: add}), nil
}

// WithRuntimeAllow adds hosts to the descriptor's one network-policy@1 runtime allow list (a singleton).
func WithRuntimeAllow(caps []CapabilityV3, hosts []string) ([]CapabilityV3, error) {
	if len(hosts) == 0 {
		return caps, nil
	}
	for i, c := range caps {
		if c.Type != CapNetworkPolicy {
			continue
		}
		cfg, err := asMap(c.Config)
		if err != nil {
			return nil, err
		}
		runtime, _ := cfg["runtime"].(map[string]any)
		if runtime == nil {
			runtime = map[string]any{}
		}
		allow, _ := runtime["allow"].([]any)
		for _, h := range hosts {
			if !slices.Contains(allow, any(h)) {
				allow = append(allow, h)
			}
		}
		runtime["allow"] = allow
		cfg["runtime"] = runtime
		caps[i].Config = cfg
		return caps, nil
	}
	return append(caps, CapabilityV3{Type: CapNetworkPolicy, Config: NetworkPolicyV3{Runtime: NetworkPhaseV3{Allow: hosts}}}), nil
}

func asMap(v any) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}
