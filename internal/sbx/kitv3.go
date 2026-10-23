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

type HookV3 struct {
	Command     string   `yaml:"command"`
	User        string   `yaml:"user,omitempty"`
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
	Entrypoint []string
	Cmd        []string
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
