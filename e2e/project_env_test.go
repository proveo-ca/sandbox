//go:build e2e

// SPEC: _spec/internal/credentials/credential-decisions.puml
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/termtext"
)

func TestProjectEnvValuesReachTheSandbox(t *testing.T) {
	requireHarness(t, "claudecode")
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte("export DOTENV_E2E_PROBE=\"from dotenv\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	k := launchKit(t, buildProveo(t), "claudecode", work, nil)
	out, _ := k.exec(`printf 'PROBE=%s\n' "${DOTENV_E2E_PROBE:-unset}"`, time.Minute)
	if !strings.Contains(out, "PROBE=from dotenv") {
		t.Fatalf("the project .env value did not reach the sandbox unquoted through --env-file:\n%s\n%s", out, k.pane(30))
	}
	if words := termtext.Words(k.pane(400)); !strings.Contains(words, "env: 1 value from ") || !strings.Contains(words, "/.env (--env-file)") {
		t.Errorf("the run never said it seeded the project .env:\n%s", k.pane(60))
	}
}
