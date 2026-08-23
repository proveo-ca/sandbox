// SPEC: _spec/internal/manifest/harness-manifest-schema.puml
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

type EnvVar struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Secret      bool   `yaml:"secret"`
}

type HomeMount struct {
	Host      string   `yaml:"host"`
	Container string   `yaml:"container"`
	Mode      string   `yaml:"mode"` // rw (default) | ro
	Deny      []string `yaml:"deny"`
}

type Home struct {
	Enabled bool        `yaml:"enabled"`
	Mounts  []HomeMount `yaml:"mounts"`
}

// Manifest describes one harness definition.
type Manifest struct {
	Name         string            `yaml:"name"`
	Description  string            `yaml:"description"`
	Egress       bool              `yaml:"egress"`       // sources the egress lifecycle
	Docker       DockerMode        `yaml:"docker"`       // how the agent gets a Docker daemon: sbx | dind | (absent)
	Provider     string            `yaml:"provider"`     // vendor-pinned broker target (firewall mode); e.g. cursor
	Subscription bool              `yaml:"subscription"` // subscription/login agent: warn, don't prompt for keys
	Stability    string            `yaml:"stability"`    // experimental | candidate | stable
	Images       map[string]string `yaml:"images"`       // target name -> image ref
	Workspace    Workspace         `yaml:"workspace"`    // mount model
	Home         Home              `yaml:"home"`         // durable ~/.proveo session/config mounts
	Env          []EnvVar          `yaml:"env"`          // secret/auth env vars the harness reads
	Config       []string          `yaml:"config"`
	Capabilities Capabilities      `yaml:"capabilities"`
	Dir          string            `yaml:"-"` // def directory (set by Load)

	// Retired flags, still parsed so a stale manifest fails loudly at load
	// instead of silently losing its docker story to the unknown-key rule.
	RetiredDind          bool `yaml:"dind"`
	RetiredSandboxDocker bool `yaml:"sandbox_docker"`
}

// DockerMode is how a harness hands its agent a Docker daemon. The two ways are
// mutually exclusive by construction: sbx runs the agent in a sandbox VM that
// has its own daemon, dind links a privileged sibling. A harness that shipped
// both would be claiming two daemons and two isolation stories at once, so the
// manifest carries ONE value rather than two booleans that have to agree.
type DockerMode string

const (
	DockerNone DockerMode = ""     // no daemon reaches the agent
	DockerSbx  DockerMode = "sbx"  // docker sandboxes (internal/sbx)
	DockerDind DockerMode = "dind" // privileged sibling sidecar (internal/dind)
)

// IsSbx reports whether this harness runs on the sandbox backend.
func (m Manifest) IsSbx() bool { return m.Docker == DockerSbx }

// IsDind reports whether this harness can get the privileged sidecar.
func (m Manifest) IsDind() bool { return m.Docker == DockerDind }

// WantsDocker reports whether the agent is promised a daemon at all — the half
// of the contract the image must honour by shipping a docker client.
func (m Manifest) WantsDocker() bool { return m.Docker != DockerNone }

type Capabilities struct {
	Egress      []string `yaml:"egress"`
	Credentials []string `yaml:"credentials"`
	Providers   []string `yaml:"providers"`
	Hosts       []string `yaml:"hosts"`
}

func (c Capabilities) AllowsEgress(mode string) bool { return listAllows(c.Egress, mode) }

func (c Capabilities) AllowsCredentials(mode string) bool { return listAllows(c.Credentials, mode) }

func (c Capabilities) AllowsProvider(name string) bool { return listAllows(c.Providers, name) }

func listAllows(list []string, v string) bool {
	if len(list) == 0 {
		return true
	}
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(v)) {
			return true
		}
	}
	return false
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
	switch m.Docker {
	case DockerNone, DockerSbx, DockerDind:
	default:
		return fmt.Errorf("manifest %q: invalid docker %q (want %q or %q)", m.Name, m.Docker, DockerSbx, DockerDind)
	}
	if m.RetiredDind || m.RetiredSandboxDocker {
		return fmt.Errorf("manifest %q: dind:/sandbox_docker: are retired — declare one docker mode instead (docker: %s or docker: %s)",
			m.Name, DockerDind, DockerSbx)
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
	for _, c := range m.Config {
		c = strings.TrimSpace(c)
		if c == "" {
			return fmt.Errorf("manifest %q: config entry with empty name", m.Name)
		}
		// config is forwarded BY VALUE, so a secret listed here would land on the
		// docker argv in plain sight. Declared secrets go through Env.
		if seen[c] {
			return fmt.Errorf("manifest %q: %q is declared in env (brokered) — it cannot also be a config passthrough", m.Name, c)
		}
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
