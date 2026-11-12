// SPEC: _spec/_devops/release-gate.puml
package contract_test

import (
	"encoding/json"
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

// gitOutput runs git in root under env; a nil env inherits the test's.
func gitOutput(root string, env []string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = env
	return cmd.Output()
}

// headTagRefusal is "" when HEAD carries a v* tag, else the one line an untagged release refuses with.
func headTagRefusal(root string, env []string) string {
	tags, err := gitOutput(root, env, "tag", "--points-at", "HEAD", "--list", "v*")
	if err != nil {
		return fmt.Sprintf("git tag --points-at HEAD: %v", err)
	}
	if strings.TrimSpace(string(tags)) != "" {
		return ""
	}
	sha, err := gitOutput(root, env, "rev-parse", "--short", "HEAD")
	if err != nil {
		return fmt.Sprintf("git rev-parse --short HEAD: %v", err)
	}
	return "HEAD " + strings.TrimSpace(string(sha)) + " carries no v* tag; deploy-cli ships tags only: " +
		"git tag vX.Y.Z, then mise run deploy-cli (mise run dev-cli previews a snapshot)"
}

const stagedLatest = "apps/cli/public/cli/latest.json"

// stagedRefusal is "" when root's staged latest.json names the version of a v* tag on HEAD,
// else the one line deploy-cli refuses with.
func stagedRefusal(root string, env []string) string {
	if why := headTagRefusal(root, env); why != "" {
		return why
	}
	out, err := gitOutput(root, env, "tag", "--points-at", "HEAD", "--list", "v*")
	if err != nil {
		return fmt.Sprintf("git tag --points-at HEAD: %v", err)
	}
	tags := strings.Fields(string(out))
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(stagedLatest)))
	if err != nil {
		return fmt.Sprintf("%s: %v; mise run release stages it", stagedLatest, err)
	}
	var latest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &latest); err != nil {
		return fmt.Sprintf("%s: %v; mise run release restages it", stagedLatest, err)
	}
	for _, tag := range tags {
		if strings.TrimPrefix(tag, "v") == latest.Version {
			return ""
		}
	}
	return fmt.Sprintf("%s names %q, not HEAD's %s; mise run release restages it from the tag",
		stagedLatest, latest.Version, strings.Join(tags, ", "))
}

func TestStagedCDNIsTheTaggedRelease(t *testing.T) {
	t.Parallel()
	releaseGate(t)
	if !envflag.On(os.Getenv(releaseGateEnv)) {
		t.Skipf("%s is off; `mise run deploy-cli` turns it on", releaseGateEnv)
	}
	if why := stagedRefusal(repoRoot(t), nil); why != "" {
		t.Fatal(why)
	}
}

func TestStagedCheckRefusesWhatTheTagDidNotBuild(t *testing.T) {
	exportDecoyGitDir(t)
	dir := t.TempDir()
	hermeticGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	hermeticGit(t, dir, "add", "a")
	hermeticGit(t, dir, "commit", "-qm", "a")
	stage := func(version string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(stagedLatest))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(`{"version": "`+version+`", "checksums": {}}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stage("0.0.1")
	if got := stagedRefusal(dir, hermeticEnv(t)); !strings.Contains(got, "carries no v* tag") {
		t.Errorf("an untagged HEAD with a staged 0.0.1 refuses with %q, want the tag refusal", got)
	}
	hermeticGit(t, dir, "tag", "v0.0.1")
	for _, version := range []string{"0.0.1-SNAPSHOT-abc1234", "dev@abc1234", "0.0.2"} {
		stage(version)
		got := stagedRefusal(dir, hermeticEnv(t))
		if !strings.Contains(got, version) || !strings.Contains(got, "mise run release") || strings.Contains(got, "\n") {
			t.Errorf("a staged %s under v0.0.1 refuses with %q, want one line naming it and mise run release", version, got)
		}
	}
	stage("0.0.1")
	if got := stagedRefusal(dir, hermeticEnv(t)); got != "" {
		t.Errorf("a staged 0.0.1 under v0.0.1 refuses with %q, want none", got)
	}
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(stagedLatest))); err != nil {
		t.Fatal(err)
	}
	if got := stagedRefusal(dir, hermeticEnv(t)); !strings.Contains(got, "mise run release") {
		t.Errorf("a missing latest.json refuses with %q, want it to name mise run release", got)
	}
}

func TestReleaseShipsATaggedHead(t *testing.T) {
	t.Parallel()
	releaseGate(t)
	if !envflag.On(os.Getenv(releaseGateEnv)) {
		t.Skipf("%s is off; `mise run release` turns it on", releaseGateEnv)
	}
	if why := headTagRefusal(repoRoot(t), nil); why != "" {
		t.Fatal(why)
	}
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

func TestTagCheckNamesTheFixOnAnUntaggedHead(t *testing.T) {
	exportDecoyGitDir(t)
	dir := t.TempDir()
	hermeticGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	hermeticGit(t, dir, "add", "a")
	hermeticGit(t, dir, "commit", "-qm", "a")

	untagged := headTagRefusal(dir, hermeticEnv(t))
	for _, want := range []string{"carries no v* tag", "git tag v", "mise run deploy-cli", "mise run dev-cli"} {
		if !strings.Contains(untagged, want) {
			t.Errorf("an untagged HEAD refuses with %q, want it to name %q", untagged, want)
		}
	}
	if strings.Contains(untagged, "\n") {
		t.Errorf("the refusal spans lines: %q", untagged)
	}

	hermeticGit(t, dir, "tag", "nightly")
	if got := headTagRefusal(dir, hermeticEnv(t)); got == "" {
		t.Error("a HEAD tagged nightly passed the tag check; only a v* tag ships")
	}

	hermeticGit(t, dir, "tag", "v0.0.1")
	if got := headTagRefusal(dir, hermeticEnv(t)); got != "" {
		t.Errorf("a HEAD tagged v0.0.1 refuses with %q, want none", got)
	}
}
