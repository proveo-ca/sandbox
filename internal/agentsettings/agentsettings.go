// Package agentsettings persists the per-harness choice matrix — the network
// tier, credential handling, and add-ons an operator picked — so a target is
// prompted for once and enters automatically afterwards.
//
// The file is keyed by target and carries a fingerprint of the def's declared
// capabilities. Cached choices deliberately do NOT survive a manifest change:
// when a def gains or loses a cell, the stale entry is discarded and the
// operator is asked again, rather than silently dropping a now-invalid add-on
// or keeping a tier the def no longer allows.
//
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

// FileName is the store's basename under the proveo home root.
const FileName = "agent-settings.yml"

// Choice is one target's settled answer for the three axes.
type Choice struct {
	Egress      string   `yaml:"egress"`
	Credentials string   `yaml:"credentials"`
	Addons      []string `yaml:"addons,omitempty"`
	// Fingerprint pins the capabilities block this answer was valid for.
	Fingerprint string `yaml:"fingerprint"`
}

// Store is the on-disk document: target name -> settled choice.
type Store struct {
	Targets map[string]Choice `yaml:"targets"`
}

// Path returns the store location under the given proveo home root.
func Path(root string) string { return filepath.Join(root, FileName) }

// Fingerprint renders a stable digest of a def's capability matrix. Any change
// to the declared cells changes the digest and so invalidates cached answers.
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

// Load reads the store. A missing file is not an error — it yields an empty
// store, which is the first-run case.
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

// Lookup returns the cached choice for target, valid only when its fingerprint
// still matches the def's current capabilities. A mismatch reports false, which
// is what forces a re-prompt.
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

// Remember records target's answer, stamping the current fingerprint.
func (s *Store) Remember(target string, c manifest.Capabilities, ch Choice) {
	if s.Targets == nil {
		s.Targets = map[string]Choice{}
	}
	ch.Fingerprint = Fingerprint(c)
	s.Targets[target] = ch
}

// Save writes the store, creating the proveo home root if needed. The file is
// 0600: it records how credentials are handled, not the credentials themselves,
// but it is still operator configuration.
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
