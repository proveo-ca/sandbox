// SPEC: _spec/internal/agentsettings/choice-cache.puml
// Package agentsettings persists the per-harness choice matrix.
package agentsettings

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/proveo-ca/proveo/internal/manifest"
)

const FileName = "agent-settings.yml"

type Choice struct {
	Egress       string            `yaml:"egress"`
	Credentials  string            `yaml:"credentials"`
	Addons       []string          `yaml:"addons,omitempty"`
	AuthVar      string            `yaml:"authVar,omitempty"`
	Evidence     string            `yaml:"evidence,omitempty"`
	LocalModel   string            `yaml:"localModel,omitempty"`
	ModelVariant string            `yaml:"modelVariant,omitempty"` // legacy: read to migrate, never written
	Models       map[string]string `yaml:"models,omitempty"`
	Fingerprint  string            `yaml:"fingerprint"`
}

type Store struct {
	Targets    map[string]Choice    `yaml:"targets"`
	Workspaces map[string]Workspace `yaml:"workspaces,omitempty"`
}

// Workspace is what one workspace path remembers, whichever harness runs it.
type Workspace struct {
	Ports []Port `yaml:"ports"`
	Apps  []App  `yaml:"apps,omitempty"`
}

// App is one Android application module the run installs on the host emulator.
type App struct {
	Module string `yaml:"module"`
	AppID  string `yaml:"appId"`
}

// Port is one published dev port and the run command that serves it.
type Port struct {
	Port    int    `yaml:"port"`
	Command string `yaml:"command"`
	Source  string `yaml:"source"`
}

// PortsFor is the remembered port answer for a workspace path; ok is false when it was never answered.
func (s *Store) PortsFor(path string) ([]Port, bool) {
	if s == nil || s.Workspaces == nil {
		return nil, false
	}
	w, ok := s.Workspaces[path]
	return w.Ports, ok
}

// RememberPorts records a workspace path's port answer; an empty answer is kept as "none".
func (s *Store) RememberPorts(path string, ports []Port) {
	if s.Workspaces == nil {
		s.Workspaces = map[string]Workspace{}
	}
	if ports == nil {
		ports = []Port{}
	}
	w := s.Workspaces[path]
	w.Ports = ports
	s.Workspaces[path] = w
}

// AppsFor is the remembered Android app answer for a workspace path; ok is false when it was never answered.
func (s *Store) AppsFor(path string) ([]App, bool) {
	if s == nil || s.Workspaces == nil {
		return nil, false
	}
	w, ok := s.Workspaces[path]
	return w.Apps, ok && w.Apps != nil
}

// RememberApps records a workspace path's Android app answer; an empty answer is kept as "none".
func (s *Store) RememberApps(path string, apps []App) {
	if s.Workspaces == nil {
		s.Workspaces = map[string]Workspace{}
	}
	if apps == nil {
		apps = []App{}
	}
	w := s.Workspaces[path]
	w.Apps = apps
	s.Workspaces[path] = w
}

func Path(root string) string { return filepath.Join(root, FileName) }

func Fingerprint(c manifest.Capabilities) string {
	norm := func(in []string) string {
		out := make([]string, 0, len(in))
		for _, v := range in {
			if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
				out = append(out, v)
			}
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		norm(c.Egress), norm(c.Credentials), norm(c.Providers),
	}, "|")))
	return hex.EncodeToString(sum[:8])
}

func Load(root string) (*Store, error) {
	s := &Store{Targets: map[string]Choice{}}
	data, err := os.ReadFile(Path(root))
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("agent settings: read: %w", err)
	}
	if err := yaml.Unmarshal(data, s); err != nil {
		return &Store{Targets: map[string]Choice{}}, fmt.Errorf("agent settings: parse %s: %w", Path(root), err)
	}
	if s.Targets == nil {
		s.Targets = map[string]Choice{}
	}
	return s, nil
}

func (s *Store) Lookup(target string, c manifest.Capabilities) (Choice, bool) {
	if s == nil || s.Targets == nil {
		return Choice{}, false
	}
	got, ok := s.Targets[target]
	if !ok || got.Fingerprint != Fingerprint(c) {
		return Choice{}, false
	}
	return got, true
}

func (s *Store) Remember(target string, c manifest.Capabilities, ch Choice) {
	if s.Targets == nil {
		s.Targets = map[string]Choice{}
	}
	ch.Fingerprint = Fingerprint(c)
	s.Targets[target] = ch
}

func (s *Store) Save(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("agent settings: mkdir %s: %w", root, err)
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("agent settings: marshal: %w", err)
	}
	if err := os.WriteFile(Path(root), data, 0o600); err != nil {
		return fmt.Errorf("agent settings: write: %w", err)
	}
	return nil
}
