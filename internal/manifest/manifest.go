// SPEC: _spec/internal/manifest/harness-manifest-schema.puml, _spec/packages/lib/config-seeding-and-persistence.puml, _spec/_paradigms/retire-dind.puml
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Filename is the manifest basename inside each def directory.
const Filename = "harness.manifest"

// Workspace declares how a harness mounts the working tree — the model the
// shims once encoded, lifted into data so `proveo run` reproduces it.
type Workspace struct {
	Layout    string `yaml:"layout"`
	ConfigDir string `yaml:"configDir"`
	GitMode   string `yaml:"gitMode"`
	Output    bool   `yaml:"output"`
	Mode      string `yaml:"mode"`
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
	// Transient names relative paths (find -path globs) the sbx config sync
	// never copies, in either direction; unlike Deny, the host keeps them.
	// SPEC: _spec/packages/lib/config-seeding-and-persistence.puml
	Transient []string `yaml:"transient"`
}

type Home struct {
	Enabled bool        `yaml:"enabled"`
	Mounts  []HomeMount `yaml:"mounts"`
	Files   []string    `yaml:"files"`
}

// Manifest describes one harness definition.
type Manifest struct {
	Name         string            `yaml:"name"`
	Description  string            `yaml:"description"`
	Subscription bool              `yaml:"subscription"` // subscription/login agent: warn, don't prompt for keys
	Stability    string            `yaml:"stability"`    // experimental | candidate | stable
	Kind         string            `yaml:"kind"`         // coding (default) | assistant: no toolchain, dependency or LSP provisioning
	Images       map[string]string `yaml:"images"`       // target name -> image ref
	Workspace    Workspace         `yaml:"workspace"`    // mount model
	Home         Home              `yaml:"home"`         // durable ~/.proveo session/config mounts
	Env          []EnvVar          `yaml:"env"`          // secret/auth env vars the harness reads
	Config       []string          `yaml:"config"`
	AgentEnv     map[string]string `yaml:"agentEnv"`
	Capabilities Capabilities      `yaml:"capabilities"`
	Dir          string            `yaml:"-"` // def directory (set by Load)

	Daemon *bool `yaml:"daemon"` // false: the sandbox promises its agent no Docker daemon
}

// KindAssistant marks a general assistant: the seed provisions no toolchain,
// dependency trees or language servers for it.
const KindAssistant = "assistant"

// IsAssistant reports whether the harness is a general assistant, not a coding agent.
func (m Manifest) IsAssistant() bool { return m.Kind == KindAssistant }

// WantsDocker reports whether the agent is promised a Docker daemon in its sandbox.
func (m Manifest) WantsDocker() bool {
	return m.Daemon == nil || *m.Daemon
}

type Capabilities struct {
	Credentials []string `yaml:"credentials"`
	Providers   []string `yaml:"providers"`
	Hosts       []string `yaml:"hosts"`
	HostBrowser string   `yaml:"hostBrowser"`
	HostDevices []string `yaml:"hostDevices"`
}

// HostBrowserCDP is the hostBrowser value for a harness that attaches to a host Chrome over CDP.
const HostBrowserCDP = "cdp"

// hostDevices values: the host emulators a harness can drive.
const (
	HostDeviceAndroid = "android"
	HostDeviceIOS     = "ios"
)

// HasHostDevice reports whether the harness declares the host device kind.
func (c Capabilities) HasHostDevice(kind string) bool { return slices.Contains(c.HostDevices, kind) }

// HasHostBrowser reports whether the harness can drive the operator's browser.
func (c Capabilities) HasHostBrowser() bool { return c.HostBrowser != "" }

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

// MissingEnv returns the declared env vars whose value is empty per getenv, in
// declaration order.
func (m Manifest) MissingEnv(getenv func(string) string) []EnvVar {
	var out []EnvVar
	for _, e := range m.Env {
		if strings.TrimSpace(getenv(e.Name)) == "" {
			out = append(out, e)
		}
	}
	return out
}

// AgentEnvPairs renders agentEnv as NAME=value in name order — a map's order
// is not an argv's, and the plan goldens read the argv — with the operator's
// own value, when they set one, in place of the default.
func (m Manifest) AgentEnvPairs(lookup func(string) string) []string {
	names := make([]string, 0, len(m.AgentEnv))
	for k := range m.AgentEnv {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, k := range names {
		v := m.AgentEnv[k]
		if lookup != nil {
			if own := strings.TrimSpace(lookup(k)); own != "" {
				v = own
			}
		}
		out = append(out, k+"="+v)
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
	for _, f := range m.Home.Files {
		name := strings.TrimSpace(f)
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\|;`) {
			return fmt.Errorf("manifest %q: home file %q must be a bare name at the home root", m.Name, f)
		}
	}
	for _, mt := range m.Home.Mounts {
		for _, p := range mt.Transient {
			if !ValidTransient(p) {
				return fmt.Errorf("manifest %q: home mount %q transient %q must be a relative path inside the mount", m.Name, mt.Host, p)
			}
		}
	}
	switch m.Kind {
	case "", "coding", KindAssistant:
	default:
		return fmt.Errorf("manifest %q: invalid kind %q (want coding or %s)", m.Name, m.Kind, KindAssistant)
	}
	switch m.Stability {
	case "", "experimental", "candidate", "stable":
	default:
		return fmt.Errorf("manifest %q: invalid stability %q", m.Name, m.Stability)
	}
	switch m.Workspace.Layout {
	case "", "app":
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
	for _, d := range m.Capabilities.HostDevices {
		if d != HostDeviceAndroid && d != HostDeviceIOS {
			return fmt.Errorf("manifest %q: invalid capabilities.hostDevices entry %q (want %s or %s)", m.Name, d, HostDeviceAndroid, HostDeviceIOS)
		}
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
	configured := map[string]bool{}
	for _, c := range m.Config {
		c = strings.TrimSpace(c)
		if c == "" {
			return fmt.Errorf("manifest %q: config entry with empty name", m.Name)
		}
		if seen[c] {
			return fmt.Errorf("manifest %q: %q is declared in env (brokered) — it cannot also be a config passthrough", m.Name, c)
		}
		configured[c] = true
	}
	for k, v := range m.AgentEnv {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("manifest %q: agentEnv entry with empty name", m.Name)
		}
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("manifest %q: agentEnv %s has no value — drop the entry rather than defaulting it to empty", m.Name, k)
		}
		if seen[k] {
			return fmt.Errorf("manifest %q: %q is declared in env — the operator supplies it, it cannot also carry a default in agentEnv", m.Name, k)
		}
		if configured[k] {
			return fmt.Errorf("manifest %q: %q is a config passthrough — it forwards the operator's value only; a default belongs in agentEnv alone", m.Name, k)
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
		if m.Container != "" && !strings.HasPrefix(m.Container, "/") {
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
		if m.Container != "" && seenCtr[m.Container] {
			return fmt.Errorf("manifest %q: duplicate home.mount container %q", name, m.Container)
		}
		seenHost[m.Host] = true
		seenCtr[m.Container] = m.Container != ""
	}
	return nil
}

// Active reports whether durable home mounts should be applied.
func (h Home) Active() bool {
	return h.Enabled && (len(h.Mounts) > 0 || len(h.Files) > 0)
}

func Parse(data []byte, dir string) (Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil && !errors.Is(err, io.EOF) {
		return Manifest{}, fmt.Errorf("manifest %s: %w", dir, err)
	}
	m.Dir = dir
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

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

// ValidTransient reports whether p is a relative, in-mount path the shell sync
// can prune: no leading slash, no "..", none of the config set's separators.
func ValidTransient(p string) bool {
	if p == "" || p != strings.TrimSpace(p) || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "|;,\\ \t\n") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}
