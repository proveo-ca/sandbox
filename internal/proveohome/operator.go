// SPEC: _spec/cmd/proveo/operator-name.puml
package proveohome

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
	// DefaultOperator is the name agents use when none is stored.
	DefaultOperator = "Executor"
	// EnvOperatorName carries the stored name into the sandbox.
	EnvOperatorName = "PROVEO_OPERATOR_NAME"
	// OperatorMaxRunes is how much of the answer is kept.
	OperatorMaxRunes = 16
	operatorFile     = "operator.yml"
)

type operatorDoc struct {
	Name string `yaml:"name"`
}

// OperatorPath is the operator file under the proveo home root.
func OperatorPath(root string) string { return filepath.Join(root, operatorFile) }

// SanitizeOperator keeps the first OperatorMaxRunes printable runes, without quotes or backslashes, trimmed.
func SanitizeOperator(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if n == OperatorMaxRunes {
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

// LoadOperator returns the stored name, or "" when none is stored.
func LoadOperator(root string) (string, error) {
	b, err := os.ReadFile(OperatorPath(root))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var d operatorDoc
	if err := yaml.Unmarshal(b, &d); err != nil {
		return "", err
	}
	return SanitizeOperator(d.Name), nil
}

// SaveOperator stores the sanitized name; an empty name removes the file, restoring the default.
func SaveOperator(root, name string) (string, error) {
	name = SanitizeOperator(name)
	if name == "" {
		if err := os.Remove(OperatorPath(root)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		return "", nil
	}
	b, err := yaml.Marshal(operatorDoc{Name: name})
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return name, os.WriteFile(OperatorPath(root), b, 0o644)
}

// OperatorEnv is the agent environment for the stored name; nothing when none is stored.
func OperatorEnv(root string) []string {
	name, err := LoadOperator(root)
	if err != nil || name == "" {
		return nil
	}
	return []string{EnvOperatorName + "=" + name}
}
