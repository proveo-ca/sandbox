// SPEC: _spec/defs/opencode/engine-local-history.puml
package contract_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeRuntimeFixtures(t *testing.T) {
	t.Parallel()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	cmd := exec.Command(python, "-B", filepath.Join(repoRoot(t), "packages", "lib", "test_opencode_runtime.py"), "-v")
	cmd.Dir = t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("OpenCode runtime regression failed: %v\n%s", err, out)
	}
}

func TestOpenCodeImageShipsOnlyTheThinRuntime(t *testing.T) {
	t.Parallel()
	body := readRepoFile(t, "defs/opencode/Dockerfile")
	for _, required := range []string{
		"COPY --chmod=0755 packages/lib/proveo-opencode-runtime /usr/local/bin/proveo-opencode-runtime",
		"exec /usr/local/bin/proveo-await-seed /usr/local/bin/proveo-opencode-runtime %s",
		`ENTRYPOINT ["dumb-init","--","/entrypoint.sh"]`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("OpenCode image is missing launch wiring: %s", required)
		}
	}
	for _, retired := range []string{"opencode-credentials.py", "proveo-opencode-preflight", "opencode_startup.py", "opencode_cache.py"} {
		if strings.Contains(body, retired) {
			t.Errorf("OpenCode image still ships the retired history keeper: %s", retired)
		}
	}
}

func TestOpenCodeDocumentsEngineLocalLogins(t *testing.T) {
	t.Parallel()
	entrypoint := readRepoFile(t, "defs/opencode/entrypoint.sh")
	if strings.Contains(entrypoint, "sandbox writes auth.json") {
		t.Fatal("OpenCode V2 still advertises obsolete plaintext provider credential storage")
	}
	if !strings.Contains(entrypoint, "Logins made inside the sandbox stay in this engine until sbx rm") {
		t.Fatal("OpenCode V2 does not say that in-sandbox logins persist in the engine")
	}
}
