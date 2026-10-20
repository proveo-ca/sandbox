// SPEC: _spec/packages/lib/seed-and-launch.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// sbxIdentityVars are the variables _proveo_in_sbx reads, in its order.
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

// sbx keeps a template's ENTRYPOINT and passes its keepalive as the command, so
// proveo's entrypoint is PID 1 there; the dispatcher's seed is the only seeder.
func TestEntrypointPassesThroughSbxKeepalive(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	lib := filepath.Join(repoRoot(t), "packages", "lib", "entrypoint-lib.sh")
	run := func(vmID string, args ...string) string {
		t.Helper()
		script := `source "$1"; shift; proveo_sbx_passthrough "$@"; echo SEEDED`
		cmd := exec.Command(bash, append([]string{"-c", script, "bash", lib}, args...)...)
		cmd.Env = append(outsideSbx(os.Environ()), "SANDBOX_VM_ID="+vmID)
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	keepalive := []string{"sh", "-c", "echo KEEPALIVE # trap 'kill -TERM -- -1; wait' TERM; sleep infinity & wait"}

	if out := run("proveo-opencode-0cae7b86", keepalive...); !strings.Contains(out, "KEEPALIVE") || strings.Contains(out, "SEEDED") {
		t.Errorf("under sbx the keepalive must be exec'd, not seeded:\n%s", out)
	}
	if out := run("", keepalive...); !strings.Contains(out, "SEEDED") {
		t.Errorf("without an sbx identity variable the entrypoint must run normally:\n%s", out)
	}
	for _, argv := range [][]string{{"opencode", "--version"}, {"sh", "-c", "echo agent"}, {}} {
		if out := run("proveo-opencode-0cae7b86", argv...); !strings.Contains(out, "SEEDED") {
			t.Errorf("argv %q is not sbx's keepalive and must run the entrypoint:\n%s", argv, out)
		}
	}
}

func TestBothSbxSitesDetectSbxByAnyIdentityVariable(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh unavailable: %v", err)
	}
	lib := filepath.Join(repoRoot(t), "packages", "lib", "entrypoint-lib.sh")
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
		"entrypoint-lib.sh proveo_sbx_passthrough": func(env []string) bool {
			script := `set -u; source "$1"; shift; proveo_sbx_passthrough "$@"; echo SEEDED`
			cmd := exec.Command(bash, "-c", script, "bash", lib, "sh", "-c", "echo KEEPALIVE # sleep infinity")
			cmd.Env = append(outsideSbx(os.Environ()), env...)
			out, _ := cmd.CombinedOutput()
			return strings.Contains(string(out), "KEEPALIVE") && !strings.Contains(string(out), "SEEDED")
		},
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

var libSourced = regexp.MustCompile(`(?s)source /entrypoint-lib\.sh\nfi\n(.*)`)

func TestHarnessEntrypointsPassThroughBeforeSeeding(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{
		"defs/cecli/entrypoint.sh",
		"defs/claudecode/mcp/entrypoint.sh",
		"defs/codex/entrypoint.sh",
		"defs/cursor/entrypoint.sh",
		"defs/opencode/entrypoint.sh",
	} {
		m := libSourced.FindStringSubmatch(readRepoFile(t, rel))
		if m == nil {
			t.Errorf("%s no longer sources /entrypoint-lib.sh in the shape this guard reads", rel)
			continue
		}
		first, _, _ := strings.Cut(strings.TrimLeft(m[1], "\n"), "\n")
		if first != `proveo_sbx_passthrough "$@"` {
			t.Errorf("%s must call proveo_sbx_passthrough \"$@\" right after sourcing the lib, got %q", rel, first)
		}
	}
}
