// SPEC: _spec/_paradigms/credential-boundary.puml, _spec/internal/sbx/state-sync.puml
package contract_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeCredentialSnapshotFixtures(t *testing.T) {
	t.Parallel()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	cmd := exec.Command(python, "-B", filepath.Join(repoRoot(t), "packages", "lib", "test_opencode_credentials.py"), "-v")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("OpenCode credential snapshot regression failed: %v\n%s", err, out)
	}
}

func TestOpenCodeImageCredentialRuntimeShim(t *testing.T) {
	t.Parallel()
	body := readRepoFile(t, "defs/opencode/Dockerfile")
	for _, required := range []string{
		"COPY --chmod=0755 packages/lib/proveo-opencode-runtime /usr/local/bin/proveo-opencode-runtime",
		"exec /usr/local/bin/proveo-await-seed /usr/local/bin/proveo-opencode-runtime %s",
		"COPY packages/lib/opencode-credentials.py /usr/local/lib/proveo/opencode-credentials.py",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("OpenCode image is missing lifecycle wiring: %s", required)
		}
	}
}

func TestOpenCodeCredentialRuntimeLifecycle(t *testing.T) {
	t.Parallel()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	cmd := exec.Command(python, "-B", filepath.Join(repoRoot(t), "packages", "lib", "test_opencode_runtime.py"), "-v")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("OpenCode credential lifecycle regression failed: %v\n%s", err, out)
	}
}

func TestOpenCodePreparedCacheFixtures(t *testing.T) {
	t.Parallel()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	cmd := exec.Command(python, "-B", filepath.Join(repoRoot(t), "packages", "lib", "test_opencode_cache.py"), "-v")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prepared cache regression: %v\n%s", err, out)
	}
}

func TestOpenCodeDoesNotAdvertisePlaintextAuthJSONPersistence(t *testing.T) {
	t.Parallel()
	entrypoint := readRepoFile(t, "defs/opencode/entrypoint.sh")
	if strings.Contains(entrypoint, "sandbox writes auth.json") {
		t.Fatal("OpenCode V2 still advertises obsolete plaintext provider credential storage")
	}
	if !strings.Contains(entrypoint, "Credentials created inside the sandbox do not persist in saved history") {
		t.Fatal("OpenCode V2 does not describe its current credential boundary")
	}
}
