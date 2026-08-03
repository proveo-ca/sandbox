// Package agentsettings persists the per-harness choice matrix.
// SPEC: _spec/_plans/harness-choice-cache.puml
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
	Egress      string   `yaml:"egress"`
	Credentials string   `yaml:"credentials"`
	Addons      []string `yaml:"addons,omitempty"`
	AuthVar     string   `yaml:"authVar,omitempty"`
	// Models is the last used model per role, keyed main/editor/small, holding
	// CANONICAL ids. Storing a harness-specific spelling would pin the entry to
	// whatever the catalog said when it was written, so a later correction could
	// never reach it. Not part of the fingerprint: a model is the operator's
	// choice, not a capability, and a manifest change must not silently drop it.
	Models      map[string]string `yaml:"models,omitempty"`
	Fingerprint string            `yaml:"fingerprint"`
}

type Store struct {
	Targets map[string]Choice `yaml:"targets"`
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
