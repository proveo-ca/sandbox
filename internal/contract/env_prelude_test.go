// SPEC: _spec/internal/sbx/sandbox-backend.puml
package contract_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/entrypoint"
)

// Inside sbx the workspace .env never becomes agent environment: the Go prelude
// and the bash prelude both key that on SANDBOX_VM_ID.
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
	start := strings.Index(src, "load_env() {")
	if start < 0 {
		t.Fatal("entrypoint-lib.sh no longer defines load_env")
	}
	body := src[start:]
	body = body[:strings.Index(body, "\n}\n")]
	if !strings.Contains(body, `[[ -n "${SANDBOX_VM_ID:-}" ]]`) {
		t.Error("load_env no longer skips the workspace .env inside sbx")
	}
	for _, dead := range []string{"PROVEO_EGRESS_MODE", "apply_broker_sentinel", "PROVEO_CREDENTIAL_BROKER_KEYS"} {
		if strings.Contains(src, dead) {
			t.Errorf("entrypoint-lib.sh still names %s, which no run sets", dead)
		}
	}
}
