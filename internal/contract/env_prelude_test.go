// SPEC: _spec/internal/sbx/sandbox-backend.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/entrypoint"
)

// Inside sbx the workspace .env never becomes agent environment: the Go prelude
// and the bash prelude both key that on SANDBOX_NAME or SANDBOX_VM_ID.
func TestBothPreludesKeepTheWorkspaceEnvOutOfTheAgentInsideSbx(t *testing.T) {
	t.Parallel()
	inside := func(k string) string {
		if k == "SANDBOX_VM_ID" {
			return "proveo-claudecode-0123abcd"
		}
		return ""
	}
	if !entrypoint.ShouldSkipEnvLoad(inside) {
		t.Error("the Go prelude loads the workspace .env inside sbx")
	}
	if entrypoint.ShouldSkipEnvLoad(func(string) string { return "" }) {
		t.Error("the Go prelude skips the .env outside sbx, where nothing else supplies it")
	}
	path := filepath.Join(repoRoot(t), "packages", "lib", "entrypoint-lib.sh")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(b)
	for _, dead := range []string{"PROVEO_EGRESS_MODE", "apply_broker_sentinel", "PROVEO_CREDENTIAL_BROKER_KEYS"} {
		if strings.Contains(src, dead) {
			t.Errorf("entrypoint-lib.sh still names %s, which no run sets", dead)
		}
	}
}

// loadEnv runs load_env under `set -euo pipefail`, the strictest way an
// entrypoint sources it (cecli, proveo-seed), in a workspace holding a .env,
// with env added to an empty environment.
func loadEnv(t *testing.T, env ...string) string {
	t.Helper()
	bash := bashOrSkip(t)
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte("PROVEO_PRELUDE_PROBE=loaded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `set -euo pipefail
source "$1/packages/lib/entrypoint-lib.sh"
cd "$2"
load_env
echo "PROBE=${PROVEO_PRELUDE_PROBE:-absent}"`
	cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t), work)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("load_env failed under set -u (%v):\n%s", err, out)
	}
	return string(out)
}

func TestTheBashPreludeLoadsTheEnvOnlyOutsideSbx(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		env  []string
		want string
	}{
		{"outside sbx, neither name set", nil, "PROBE=loaded"},
		{"outside sbx, names blank", []string{"SANDBOX_NAME= ", "SANDBOX_VM_ID="}, "PROBE=loaded"},
		{"inside sbx by SANDBOX_NAME", []string{"SANDBOX_NAME=proveo-cecli-0123abcd"}, "PROBE=absent"},
		{"inside sbx by SANDBOX_VM_ID", []string{"SANDBOX_VM_ID=proveo-cecli-0123abcd"}, "PROBE=absent"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if out := loadEnv(t, c.env...); !strings.Contains(out, c.want) {
				t.Errorf("want %s:\n%s", c.want, out)
			}
		})
	}
}
