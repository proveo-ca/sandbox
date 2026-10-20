// SPEC: _spec/internal/credentials/credential-decisions.puml
package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/provider"
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// seedProjectEnv writes the project .env at src as an sbx --env-file under runDir and returns
// its path and how many values it holds. Provider keys stay out unless the run forwards them;
// names in set (KEY=value or KEY) and values a line cannot carry are skipped.
func seedProjectEnv(src, runDir string, forwards bool, set []string) (string, int, error) {
	if src == "" {
		return "", 0, nil
	}
	vals := credentials.ParseEnvFile(src)
	skip := map[string]bool{}
	for _, e := range set {
		name, _, _ := strings.Cut(e, "=")
		skip[name] = true
	}
	keys := provider.KeyVars()
	var names []string
	for k, v := range vals {
		switch {
		case skip[k], !envName.MatchString(k), strings.ContainsAny(v, "\r\n"):
		case !forwards && slices.Contains(keys, k):
		default:
			names = append(names, k)
		}
	}
	if len(names) == 0 {
		return "", 0, nil
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		b.WriteString(k + "=" + vals[k] + "\n")
	}
	dir := filepath.Join(runDir, "sbx")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	path := filepath.Join(dir, "env")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", 0, err
	}
	return path, len(names), os.Chmod(path, 0o600)
}
