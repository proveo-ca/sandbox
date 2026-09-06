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
	// Credentials may only be declared by a kit that IS the agent. A mixin
	// repeating a service its parent already declares is refused outright —
	// measured: `400 ... credential for service "anthropic" defined in both
	// "shell" and "credprobe"`. SPEC: _spec/_experiments/sbx-kit-capabilities.puml
	Credentials []KitCredential `yaml:"credentials,omitempty"`
}

// KitCredential is one service sbx should hold on the agent's behalf.
//
// This is the half a def LOSES by declaring its own agent. A built-in agent
// declares its credentials, so sbx sets the env var to a sentinel and injects
// the real value host-side per request — measured on the stock shell agent as
// `VALUE: proxy-managed`. An agent that declares none gets no such treatment
// and the real key lands in the process.
type KitCredential struct {
	Service  string         `yaml:"service"`
	Required bool           `yaml:"required,omitempty"`
	APIKey   *KitCredAPIKey `yaml:"apiKey,omitempty"`
}

// KitCredAPIKey names the env var to sentinel and where the value may go.
type KitCredAPIKey struct {
	Name string `yaml:"name"`
	// ProxyManaged is the entire point: with it the named variable holds a
	// sentinel in the agent process and the host-side proxy attaches the real
	// value. Without it the variable holds the credential.
	ProxyManaged bool            `yaml:"proxyManaged,omitempty"`
	Inject       []KitCredInject `yaml:"inject,omitempty"`
}

// KitCredInject is one destination the value may be attached to. SPEC-v2:
// every domain here MUST also appear in permissions.network.allow.
type KitCredInject struct {
	Domain string `yaml:"domain"`
	Header string `yaml:"header"`
	Format string `yaml:"format,omitempty"`
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
