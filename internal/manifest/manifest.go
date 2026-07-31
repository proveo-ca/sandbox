// Package manifest reads the per-harness `defs/<name>/harness.manifest` files —
// the single registration point. Adding a harness should mean dropping
// a def dir with a manifest; nothing else enumerates harnesses by hand.
package manifest

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Filename is the manifest basename inside each def directory.
const Filename = "harness.manifest"

// Workspace declares how a harness mounts the working tree — the model the
// run.sh files encode today, lifted into data so `proveo run` can reproduce it.
type Workspace struct {
	// Layout: "app" (mount the repo at /app, -w /app; the monorepo model used by
	// cursor/opencode/cecli) or "input-output" (claudecode: /workspace/input +
	// /workspace/output).
	Layout string `yaml:"layout"`
	// ConfigDir is the tool config dir preserved from the repo root in the
	// monorepo-subdir case (e.g. ".cursor", ".opencode", ".cecli"). app layout only.
	ConfigDir string `yaml:"configDir"`
	// GitMode is how the root .git is mounted in the subdir case: "rw" (default)
	// or "ro" (cecli). app layout only.
	GitMode string `yaml:"gitMode"`
	// Output mounts the output dir at /app/output:rw (cecli). app layout only.
	Output bool `yaml:"output"`
	// Mode is how the working tree itself is mounted: "rw" (default) or "ro".
	// Applies to app (/app) and input-output (/workspace/input).
	Mode string `yaml:"mode"`
}

// EnvVar declares an environment variable a harness reads at run time, so the
// CLI can forward it into the container and — when it is missing — prompt for
// it (interactive wizard) or warn (non-TTY). Secret values are prompted with
// echo off and are only ever forwarded by name (`-e NAME`), never on an argv.
type EnvVar struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Secret      bool   `yaml:"secret"`
}

// HomeMount is one bind under PROVEO_HOME (default ~/.proveo) into the container.
// Host is relative to PROVEO_HOME; Container is an absolute path (typically under
// /proveo-home). Deny lists basenames scrubbed from the host dir before each run
// so login tokens never accumulate in the durable session cache.
type HomeMount struct {
	Host      string   `yaml:"host"`
	Container string   `yaml:"container"`
	Mode      string   `yaml:"mode"` // rw (default) | ro
	Deny      []string `yaml:"deny"`
}

// Home declares durable proveo-owned session/config mounts. Enabled with at
// least one mount; proveo sets HOME=/proveo-home when Home is active so tools
// write into the mounted tree regardless of the run-as uid.
type Home struct {
	Enabled bool        `yaml:"enabled"`
	Mounts  []HomeMount `yaml:"mounts"`
}

// Manifest describes one harness definition.
type Manifest struct {
	Name         string            `yaml:"name"`
	Description  string            `yaml:"description"`
	Egress       bool              `yaml:"egress"`       // sources the egress lifecycle
	Dind         bool              `yaml:"dind"`         // image ships docker client; may get DinD sidecar
	Provider     string            `yaml:"provider"`     // vendor-pinned broker target (firewall mode); e.g. cursor
	Subscription bool              `yaml:"subscription"` // subscription/login agent: warn, don't prompt for keys
	Stability    string            `yaml:"stability"`    // experimental | candidate | stable
	Images       map[string]string `yaml:"images"`       // target name -> image ref
	Workspace    Workspace         `yaml:"workspace"`    // mount model
	Home         Home              `yaml:"home"`         // durable ~/.proveo session/config mounts
	Env          []EnvVar          `yaml:"env"`          // env vars the harness reads
	Dir          string            `yaml:"-"`            // def directory (set by Load)
}

// MissingEnv returns the declared env vars whose value is empty per getenv,
// in declaration order.
func (m Manifest) MissingEnv(getenv func(string) string) []EnvVar {
	var out []EnvVar
	for _, e := range m.Env {
		if strings.TrimSpace(getenv(e.Name)) == "" {
			out = append(out, e)
		}
	}
	return out
}

// Validate reports whether a manifest is well-formed.
func (m Manifest) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("manifest %s: missing name", m.Dir)
	}
	if len(m.Images) == 0 {
		return fmt.Errorf("manifest %q: at least one images entry is required", m.Name)
	}
	for target, image := range m.Images {
		if target == "" || image == "" {
			return fmt.Errorf("manifest %q: empty target or image (%q: %q)", m.Name, target, image)
		}
	}
	switch m.Stability {
	case "", "experimental", "candidate", "stable":
	default:
		return fmt.Errorf("manifest %q: invalid stability %q", m.Name, m.Stability)
	}
	switch m.Workspace.Layout {
	case "", "app", "input-output":
	default:
		return fmt.Errorf("manifest %q: invalid workspace.layout %q", m.Name, m.Workspace.Layout)
	}
	switch m.Workspace.GitMode {
	case "", "rw", "ro":
	default:
		return fmt.Errorf("manifest %q: invalid workspace.gitMode %q", m.Name, m.Workspace.GitMode)
	}
	switch m.Workspace.Mode {
	case "", "rw", "ro":
	default:
		return fmt.Errorf("manifest %q: invalid workspace.mode %q", m.Name, m.Workspace.Mode)
	}
	seen := map[string]bool{}
	for _, e := range m.Env {
		if e.Name == "" {
			return fmt.Errorf("manifest %q: env entry with empty name", m.Name)
		}
		if seen[e.Name] {
			return fmt.Errorf("manifest %q: duplicate env entry %q", m.Name, e.Name)
		}
		seen[e.Name] = true
	}
	if err := m.Home.validate(m.Name); err != nil {
		return err
	}
	return nil
}

func (h Home) validate(name string) error {
	if !h.Enabled && len(h.Mounts) == 0 {
		return nil
	}
	if h.Enabled && len(h.Mounts) == 0 {
		return fmt.Errorf("manifest %q: home.enabled with no mounts", name)
	}
	seenHost := map[string]bool{}
	seenCtr := map[string]bool{}
	for i, m := range h.Mounts {
		if strings.TrimSpace(m.Host) == "" {
			return fmt.Errorf("manifest %q: home.mounts[%d]: empty host", name, i)
		}
		if filepath.IsAbs(m.Host) || strings.Contains(m.Host, "..") {
			return fmt.Errorf("manifest %q: home.mounts[%d]: host %q must be relative to PROVEO_HOME (no ..)", name, i, m.Host)
		}
		if !strings.HasPrefix(m.Container, "/") {
			return fmt.Errorf("manifest %q: home.mounts[%d]: container %q must be absolute", name, i, m.Container)
		}
		switch m.Mode {
		case "", "rw", "ro":
		default:
			return fmt.Errorf("manifest %q: home.mounts[%d]: invalid mode %q", name, i, m.Mode)
		}
		if seenHost[m.Host] {
			return fmt.Errorf("manifest %q: duplicate home.mount host %q", name, m.Host)
		}
		if seenCtr[m.Container] {
			return fmt.Errorf("manifest %q: duplicate home.mount container %q", name, m.Container)
		}
		seenHost[m.Host] = true
		seenCtr[m.Container] = true
	}
	return nil
}

// Active reports whether durable home mounts should be applied.
func (h Home) Active() bool {
	return h.Enabled && len(h.Mounts) > 0
}

// Parse decodes one manifest from YAML bytes (dir is used only for messages).
func Parse(data []byte, dir string) (Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("manifest %s: %w", dir, err)
	}
	m.Dir = dir
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Load reads every `defs/*/harness.manifest` under defsDir, sorted by name.
func Load(defsDir string) ([]Manifest, error) {
	matches, err := filepath.Glob(filepath.Join(defsDir, "*", Filename))
	if err != nil {
		return nil, err
	}
	var out []Manifest
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		m, err := Parse(data, filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// LoadFS reads every defs/*/harness.manifest from an fs.FS (e.g. the embedded
// manifests), so the CLI works without the defs tree on disk.
func LoadFS(fsys fs.FS) ([]Manifest, error) {
	matches, err := fs.Glob(fsys, "defs/*/"+Filename)
	if err != nil {
		return nil, err
	}
	var out []Manifest
	for _, p := range matches {
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil, err
		}
		m, err := Parse(data, path.Dir(p))
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Targets flattens the images across manifests into target -> image, erroring on
// a duplicate target name (two harnesses claiming the same runnable target).
func Targets(ms []Manifest) (map[string]string, error) {
	out := make(map[string]string)
	for _, m := range ms {
		for target, image := range m.Images {
			if prev, dup := out[target]; dup {
				return nil, fmt.Errorf("duplicate target %q (%q and %q)", target, prev, image)
			}
			out[target] = image
		}
	}
	return out, nil
}
