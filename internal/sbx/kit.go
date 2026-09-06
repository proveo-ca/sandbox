package sbx

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Kit struct {
	SchemaVersion string         `yaml:"schemaVersion"`
	Kind          string         `yaml:"kind"`
	Name          string         `yaml:"name"`
	DisplayName   string         `yaml:"displayName,omitempty"`
	Description   string         `yaml:"description,omitempty"`
	Permissions   KitPermissions `yaml:"permissions,omitempty"`
	Environment   *KitEnv        `yaml:"environment,omitempty"`
	Setup         *KitSetup      `yaml:"setup,omitempty"`
	// Sandbox turns this Kit from a mixin into a COMPLETE AGENT, and is the
	// field that lets a harness sbx ships no agent for stop borrowing one.
	// SPEC: _spec/_experiments/sbx-kit-capabilities.puml
	Sandbox *KitSandbox `yaml:"sandbox,omitempty"`
}

// KitEnv carries values RESOLVED ON THE HOST.
type KitEnv struct {
	Variables map[string]string `yaml:"variables,omitempty"`
}

// KitSetup holds the container-side steps.
type KitSetup struct {
	Startup []KitCommand `yaml:"startup,omitempty"`
}

// KitCommand is one setup step. `startup` takes a LIST command (install takes a
// string) — the two spellings differ and the loader is strict about it.
type KitCommand struct {
	Command     []string `yaml:"command"`
	User        string   `yaml:"user,omitempty"`
	Description string   `yaml:"description,omitempty"`
}

func SeedCommand(target string) KitCommand {
	return KitCommand{
		Command:     []string{"/usr/local/bin/proveo-seed", target},
		User:        "1000",
		Description: "proveo: compose subagents, settings and workspace trust",
	}
}

// KitSandbox names the image and what runs in it. SPEC-v2: "A sandbox kit is a
// COMPLETE AGENT. It MUST declare a `sandbox:` block and MAY declare every
// shared block", and its `name` is what `sbx run <name> --kit <path>` selects.
//
// Entrypoint and Command are BOTH omitempty on purpose. The def's image already
// declares ENTRYPOINT ["dumb-init", "--", "<target>-entrypoint"] and CMD
// ["<target>"], and that pair is what the docker backend runs and what works
// there. Restating it here would be a second copy free to drift from the first,
// so the Kit names the image and lets the image speak. If a measurement shows
// sbx requires the prefix explicitly, it goes in then and not before.
type KitSandbox struct {
	Image      string             `yaml:"image"`
	Entrypoint []string           `yaml:"entrypoint,omitempty"`
	Command    *KitSandboxCommand `yaml:"command,omitempty"`
}

// KitSandboxCommand is the mode-specific tail appended to the entrypoint.
type KitSandboxCommand struct {
	Default     []string `yaml:"default,omitempty"`
	Interactive []string `yaml:"interactive,omitempty"`
}

// KitPermissions carries the network policy.
type KitPermissions struct {
	Network KitNet `yaml:"network,omitempty"`
}

// KitNet is the egress allowlist, declared rather than enforced by a sidecar.
type KitNet struct {
	Allow []string `yaml:"allow,omitempty"`
	Deny  []string `yaml:"deny,omitempty"`
}

func WriteKit(dir string, k Kit) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(k); err != nil {
		return "", fmt.Errorf("sbx kit encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("sbx kit encode: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("sbx kit dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec.yaml"), buf.Bytes(), 0o600); err != nil {
		return "", fmt.Errorf("sbx kit write: %w", err)
	}
	return dir, nil
}
