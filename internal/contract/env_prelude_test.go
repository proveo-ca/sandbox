// SPEC: _spec/internal/sbx/sandbox-backend.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const preludeProbe = `echo "PROBE=${PROVEO_PROBE:-unset} GEMINI=${GEMINI_API_KEY:-unset} ` +
	`GOOGLE_API=${GOOGLE_API_KEY:-unset} GOOGLE_GEN=${GOOGLE_GENERATIVE_AI_API_KEY:-unset}"`

// leakyWorkspace returns a temp dir holding a .env that sets PROVEO_PROBE=leaked.
func leakyWorkspace(t *testing.T) string {
	t.Helper()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte("PROVEO_PROBE=leaked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return work
}

// bashPrelude runs the entrypoints' shell prelude under `set -euo pipefail` from work, then the probe.
func bashPrelude(t *testing.T, work, steps string, env ...string) string {
	t.Helper()
	script := `set -euo pipefail
source "$1/packages/lib/entrypoint-lib.sh"
` + steps + `
` + preludeProbe
	cmd := exec.Command(bashOrSkip(t), "-c", script, "bash", repoRoot(t), work)
	cmd.Dir = work
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash prelude failed under set -u (%v):\n%s", err, out)
	}
	return string(out)
}

// buildEntrypoint builds cmd/proveo-entrypoint into a temp dir.
func buildEntrypoint(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go unavailable: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "proveo-entrypoint")
	build := exec.Command(goBin, "build", "-o", bin, "./cmd/proveo-entrypoint")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build proveo-entrypoint: %v\n%s", err, out)
	}
	return bin
}

// goPrelude runs `proveo-entrypoint probe -- /bin/sh -c <probe>` from work with an empty PATH.
func goPrelude(t *testing.T, bin, work string, env ...string) string {
	t.Helper()
	cmd := exec.Command(bin, "probe", "--", "/bin/sh", "-c", preludeProbe)
	cmd.Dir = work
	cmd.Env = append([]string{"PATH=", "HOME=" + t.TempDir(), "PROVEO_WORKDIR=" + work}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo-entrypoint prelude failed (%v):\n%s", err, out)
	}
	return string(out)
}

func TestNeitherPreludeReadsTheWorkspaceEnv(t *testing.T) {
	t.Parallel()
	bin := buildEntrypoint(t)
	const steps = `set_working_directory "$2"
bridge_google_keys
bridge_git_identity
apply_env_bridges`
	for _, c := range []struct {
		name string
		env  []string
	}{
		{"outside sbx", nil},
		{"inside sbx by SANDBOX_NAME", []string{"SANDBOX_NAME=proveo-cecli-0123abcd"}},
		{"inside sbx by SANDBOX_VM_ID", []string{"SANDBOX_VM_ID=proveo-cecli-0123abcd"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			work := leakyWorkspace(t)
			for prelude, out := range map[string]string{
				"bash prelude":           bashPrelude(t, work, steps, c.env...),
				"proveo-entrypoint prep": goPrelude(t, bin, work, c.env...),
			} {
				if !strings.Contains(out, "PROBE=unset") || strings.Contains(out, "leaked") {
					t.Errorf("%s exported the workspace .env:\n%s", prelude, out)
				}
				if strings.Contains(out, ".env") {
					t.Errorf("%s still reports on a workspace .env:\n%s", prelude, out)
				}
			}
		})
	}
}

func TestBothPreludesBridgeTheGoogleKeyAliases(t *testing.T) {
	t.Parallel()
	bin := buildEntrypoint(t)
	work := t.TempDir()
	for _, c := range []struct {
		name, env, want string
	}{
		{"from GEMINI_API_KEY", "GEMINI_API_KEY=g-key", "GEMINI=g-key GOOGLE_API=g-key GOOGLE_GEN=g-key"},
		{"from GOOGLE_API_KEY", "GOOGLE_API_KEY=a-key", "GEMINI=a-key GOOGLE_API=a-key GOOGLE_GEN=a-key"},
		{"from GOOGLE_GENERATIVE_AI_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY=v-key", "GEMINI=v-key GOOGLE_API=v-key GOOGLE_GEN=v-key"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			for prelude, out := range map[string]string{
				"bash bridge_google_keys": bashPrelude(t, work, "bridge_google_keys", c.env),
				"proveo-entrypoint prep":  goPrelude(t, bin, work, c.env),
			} {
				if !strings.Contains(out, c.want) {
					t.Errorf("%s: want %q:\n%s", prelude, c.want, out)
				}
			}
		})
	}
}

func TestEntrypointLibNamesNoRetiredRunSetting(t *testing.T) {
	t.Parallel()
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
