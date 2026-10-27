// SPEC: _spec/packages/lib/seed-and-launch.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// sbxIdentityVars are the variables proveo-await-seed reads to detect sbx.
var sbxIdentityVars = []string{"SANDBOX_NAME", "SANDBOX_ID", "SANDBOX_VM_ID"}

// outsideSbx returns env without any sbx identity variable.
func outsideSbx(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !slices.Contains(sbxIdentityVars, k) {
			out = append(out, kv)
		}
	}
	return out
}

func TestAwaitSeedDetectsSbxByAnyIdentityVariable(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh unavailable: %v", err)
	}
	await := filepath.Join(repoRoot(t), "packages", "lib", "proveo-await-seed")
	dir := t.TempDir()
	marker, refused := filepath.Join(dir, "seeded"), filepath.Join(dir, "refused")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(refused, []byte("probe refusal\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sites := map[string]func(env []string) bool{
		"proveo-await-seed": func(env []string) bool {
			cmd := exec.Command(sh, "-u", await, "echo", "LAUNCHED")
			cmd.Env = append(outsideSbx(os.Environ()), append([]string{"PROVEO_WORKDIR=" + dir,
				"PROVEO_INSTRUCTIONS_MARKER=" + marker, "PROVEO_SEED_REFUSED=" + refused}, env...)...)
			out, err := cmd.CombinedOutput()
			return err != nil && strings.Contains(string(out), "did not release") && !strings.Contains(string(out), "LAUNCHED")
		},
	}
	for _, c := range []struct {
		name  string
		env   []string
		inSbx bool
	}{
		{"SANDBOX_NAME alone", []string{"SANDBOX_NAME=proveo-codex-0123abcd"}, true},
		{"SANDBOX_ID alone", []string{"SANDBOX_ID=0123abcd-4567"}, true},
		{"SANDBOX_VM_ID alone", []string{"SANDBOX_VM_ID=proveo-codex-0123abcd"}, true},
		{"none of them", nil, false},
		{"only blanks", []string{"SANDBOX_NAME=", "SANDBOX_ID=", "SANDBOX_VM_ID="}, false},
	} {
		for site, sees := range sites {
			if got := sees(c.env); got != c.inSbx {
				t.Errorf("%s with %s: sees sbx=%v, want %v", site, c.name, got, c.inSbx)
			}
		}
	}
}
