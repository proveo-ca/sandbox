// SPEC: _spec/cmd/proveo/operator-name.puml
package operator

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	// Default is the name agents use when none is stored.
	Default = "Executor"
	// EnvName carries the stored name into the sandbox.
	EnvName = "PROVEO_OPERATOR_NAME"
	// MaxRunes is how much of the answer is kept.
	MaxRunes = 16
	file     = "operator.yml"
)

type doc struct {
	Name string `yaml:"name"`
}

// Path is the operator file under the proveo home root.
func Path(root string) string { return filepath.Join(root, file) }

// Sanitize keeps the first MaxRunes printable runes, without quotes or backslashes, trimmed.
func Sanitize(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if n == MaxRunes {
			break
		}
		if r == '"' || r == '\\' || r == '`' || !unicode.IsPrint(r) {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// Load returns the stored name, or "" when none is stored.
func Load(root string) (string, error) {
	b, err := os.ReadFile(Path(root))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var d doc
	if err := yaml.Unmarshal(b, &d); err != nil {
		return "", err
	}
	return Sanitize(d.Name), nil
}

// Save stores the sanitized name; an empty name removes the file, restoring the default.
func Save(root, name string) (string, error) {
	name = Sanitize(name)
	if name == "" {
		if err := os.Remove(Path(root)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		return "", nil
	}
	b, err := yaml.Marshal(doc{Name: name})
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return name, os.WriteFile(Path(root), b, 0o644)
}

// Env is the agent environment for the stored name; nothing when none is stored.
func Env(root string) []string {
	name, err := Load(root)
	if err != nil || name == "" {
		return nil
	}
	return []string{EnvName + "=" + name}
}
