// SPEC: _spec/internal/sbx/sandbox-backend.puml, _spec/cmd/proveo-entrypoint/prep-process-boundary.puml
package contract_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// gitOnlyPath returns a PATH directory holding only git; empty without git.
func gitOnlyPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if git, err := exec.LookPath("git"); err == nil {
		if err := os.Symlink(git, filepath.Join(dir, "git")); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runEntrypoint runs `proveo-entrypoint <args>` from dir with a git-only PATH, an empty HOME and env.
func runEntrypoint(t *testing.T, bin, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + gitOnlyPath(t), "HOME=" + t.TempDir(),
		"XDG_CONFIG_HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// gitRepo returns the resolved path of a fresh git repository.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

func TestNeitherPreludeReadsTheWorkspaceEnv(t *testing.T) {
	t.Parallel()
	bin := buildEntrypoint(t)
	const steps = `set_working_directory "$2"
bridge_google_keys
bridge_git_identity`
	for _, c := range []struct {
		name string
		env  []string
	}{
		{"outside sbx", nil},
		{"inside sbx by SANDBOX_NAME", []string{"SANDBOX_NAME=proveo-hermes-0123abcd"}},
		{"inside sbx by SANDBOX_VM_ID", []string{"SANDBOX_VM_ID=proveo-hermes-0123abcd"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			work := leakyWorkspace(t)
			out := bashPrelude(t, work, steps, c.env...)
			if !strings.Contains(out, "PROBE=unset") || strings.Contains(out, "leaked") {
				t.Errorf("bash prelude exported the workspace .env:\n%s", out)
			}
			if strings.Contains(out, ".env") {
				t.Errorf("bash prelude still reports on a workspace .env:\n%s", out)
			}
			if out, err := runEntrypoint(t, bin, work, append([]string{"PROVEO_WORKDIR=" + work}, c.env...), "prep"); err != nil || strings.Contains(out, ".env") {
				t.Errorf("proveo-entrypoint prep reports on a workspace .env (%v):\n%s", err, out)
			}
		})
	}
}

func TestBashPreludeBridgesTheGoogleKeyAliases(t *testing.T) {
	t.Parallel()
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
			if out := bashPrelude(t, work, "bridge_google_keys", c.env); !strings.Contains(out, c.want) {
				t.Errorf("bridge_google_keys: want %q:\n%s", c.want, out)
			}
		})
	}
}

func TestPrepKeepsOnlyWhatCrossesTheBoundary(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	bin := buildEntrypoint(t)
	repo := gitRepo(t)

	if out, err := runEntrypoint(t, bin, repo, nil, "probe", "--", "/bin/sh", "-c", "echo EXEC_MODE_RAN"); err == nil || strings.Contains(out, "EXEC_MODE_RAN") {
		t.Errorf("proveo-entrypoint <target> -- <command> still runs the command (%v):\n%s", err, out)
	}

	for _, c := range []struct {
		name, want, reject string
		env                []string
	}{
		{"reports the cwd it inherits", "Git repository at " + repo, "Not a git repository",
			[]string{"PROVEO_WORKDIR=" + t.TempDir()}},
		{"reports the identity the shell bridged", "Git identity: Shell Name <shell@example.com>", "No git identity",
			[]string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=user.name", "GIT_CONFIG_VALUE_0=Shell Name",
				"GIT_CONFIG_KEY_1=user.email", "GIT_CONFIG_VALUE_1=shell@example.com"}},
		{"bridges no identity itself", "No git identity", "Author Name",
			[]string{"GIT_AUTHOR_NAME=Author Name", "GIT_AUTHOR_EMAIL=author@example.com"}},
	} {
		out, err := runEntrypoint(t, bin, repo, c.env, "prep")
		if err != nil || !strings.Contains(out, c.want) || strings.Contains(out, c.reject) {
			t.Errorf("prep %s: want %q and no %q (%v):\n%s", c.name, c.want, c.reject, err, out)
		}
	}
}

func TestPrepTakesNoTargetAndNoSmokeMode(t *testing.T) {
	t.Parallel()
	bin := buildEntrypoint(t)
	dir := t.TempDir()
	if out, err := runEntrypoint(t, bin, dir, nil, "prep", "probe"); err == nil {
		t.Errorf("prep accepted a target argument:\n%s", out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "prep")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + gitOnlyPath(t), "HOME=" + t.TempDir(), "PROVEO_SMOKE_TEST=1", "PROVEO_SMOKE_TARGET=probe"}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil || err != nil || strings.Contains(string(out), "PROVEO_SMOKE_READY") {
		t.Errorf("prep under PROVEO_SMOKE_TEST=1 must return without a sentinel (ctx %v, err %v):\n%s", ctx.Err(), err, out)
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
