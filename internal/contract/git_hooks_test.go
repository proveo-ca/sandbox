// SPEC: _spec/_devops/git-hooks.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostCommitRunsFormatThenLint(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "scripts/githooks/post-commit")
	fmtAt := strings.Index(src, "mise run fmt")
	lintAt := strings.Index(src, "mise run lint")
	if fmtAt < 0 || lintAt < 0 {
		t.Fatalf("post-commit must call mise run fmt and mise run lint:\n%s", src)
	}
	if fmtAt > lintAt {
		t.Error("format must run before lint: lint's first gate is gofmt -l")
	}
	for _, banned := range []string{"git commit", "git config", "git add", "--amend"} {
		if strings.Contains(src, banned) {
			t.Errorf("post-commit contains %q — that loops or mutates git config", banned)
		}
	}
	if !strings.Contains(src, "SKIP_GIT_HOOKS") {
		t.Error("post-commit must honour SKIP_GIT_HOOKS so CI can skip the gate")
	}
}

func TestInstallGitHooksCopiesPostCommitWithoutGitConfig(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "scripts/githooks/install")
	for _, want := range []string{
		"scripts/githooks/post-commit",
		".git/hooks/post-commit",
		"chmod 0755",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("install lacks %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "git config") || strings.Contains(src, "hooksPath") {
		t.Error("install must not set core.hooksPath")
	}
}

func TestMiseDeclaresInstallGitHooks(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "mise.toml")
	if !strings.Contains(src, "[tasks.install-git-hooks]") {
		t.Fatal("mise.toml must declare install-git-hooks")
	}
	if !strings.Contains(src, "scripts/githooks/install") {
		t.Error("install-git-hooks must run scripts/githooks/install")
	}
}

func TestInstallGitHooksWritesAnExecutablePostCommit(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	hookSrc := filepath.Join(root, "scripts", "githooks")
	if err := os.MkdirAll(hookSrc, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "githooks", "post-commit"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookSrc, "post-commit"), body, 0o755); err != nil {
		t.Fatal(err)
	}
	inst, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "githooks", "install"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookSrc, "install"), inst, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	cmd := exec.Command(bash, filepath.Join(hookSrc, "install"))
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	dst := filepath.Join(root, ".git", "hooks", "post-commit")
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("post-commit was not installed: %v\n%s", err, out)
	}
	if st.Mode()&0o111 == 0 {
		t.Errorf("installed post-commit is not executable: %o", st.Mode())
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Error("installed hook is not the versioned script")
	}
}
