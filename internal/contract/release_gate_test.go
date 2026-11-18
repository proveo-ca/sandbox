// SPEC: _spec/_devops/release-gate.puml
package contract_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/envflag"
)

const releaseGateEnv = "PROVEO_RELEASE_GATE"

// releaseGate fails t at cleanup when t skipped while PROVEO_RELEASE_GATE is on.
func releaseGate(t testing.TB) {
	t.Helper()
	t.Cleanup(func() {
		if t.Skipped() && envflag.On(os.Getenv(releaseGateEnv)) {
			t.Errorf("skipped under %s: the release gate takes a skip as a failure", releaseGateEnv)
		}
	})
}

// gateTB records what a test helper does to a test without running one.
type gateTB struct {
	testing.TB
	skipped  bool
	errs     []string
	fatals   []string
	skips    []string
	cleanups []func()
}

func (f *gateTB) Helper()                   {}
func (f *gateTB) Cleanup(fn func())         { f.cleanups = append(f.cleanups, fn) }
func (f *gateTB) Skipped() bool             { return f.skipped }
func (f *gateTB) Errorf(s string, a ...any) { f.errs = append(f.errs, fmt.Sprintf(s, a...)) }
func (f *gateTB) Fatalf(s string, a ...any) { f.fatals = append(f.fatals, fmt.Sprintf(s, a...)) }
func (f *gateTB) Skipf(s string, a ...any)  { f.skips = append(f.skips, fmt.Sprintf(s, a...)) }

func TestReleaseGateTurnsASkipIntoAFailure(t *testing.T) {
	for _, c := range []struct {
		gate    string
		skipped bool
		errs    int
	}{
		{gate: "1", skipped: true, errs: 1},
		{gate: "on", skipped: false, errs: 0},
		{gate: "", skipped: true, errs: 0},
		{gate: "0", skipped: true, errs: 0},
	} {
		t.Setenv(releaseGateEnv, c.gate)
		fake := &gateTB{skipped: c.skipped}
		releaseGate(fake)
		if len(fake.cleanups) != 1 {
			t.Fatalf("releaseGate registered %d cleanups, want 1", len(fake.cleanups))
		}
		fake.cleanups[0]()
		if len(fake.errs) != c.errs {
			t.Errorf("%s=%q, skipped=%v: releaseGate reported %q, want %d errors", releaseGateEnv, c.gate, c.skipped, fake.errs, c.errs)
		}
	}
}

// hermeticEnv is the test's environment minus GIT_*, HOME and XDG_CONFIG_HOME, with a throwaway
// HOME and no global or system git config.
func hermeticEnv(t *testing.T) []string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			env = append(env, kv)
		}
	}
	return append(env, "HOME="+t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
}

// hermeticGit runs git in dir under hermeticEnv with a fixed identity.
func hermeticGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false",
		"-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = hermeticEnv(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// exportDecoyGitDir points GIT_DIR at a fresh repo whose HEAD tracks decoy.go and carries v9.9.9.
func exportDecoyGitDir(t *testing.T) {
	t.Helper()
	decoy := t.TempDir()
	hermeticGit(t, decoy, "init", "-q")
	if err := os.WriteFile(filepath.Join(decoy, "decoy.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hermeticGit(t, decoy, "add", "decoy.go")
	hermeticGit(t, decoy, "commit", "-qm", "decoy")
	hermeticGit(t, decoy, "tag", "v9.9.9")
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
}
