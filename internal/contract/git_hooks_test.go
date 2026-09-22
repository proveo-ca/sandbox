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
	src := readRepoFile(t, "cmd/proveo-dev/hooks.go")
	fmtAt := strings.Index(src, `"mise", "run", "fmt"`)
	lintAt := strings.Index(src, `"mise", "run", "lint"`)
	if fmtAt < 0 || lintAt < 0 {
		t.Fatal("proveo-dev hooks post-commit must run mise run fmt and mise run lint")
	}
	if fmtAt > lintAt {
		t.Error("format must run before lint: lint's first gate is gofmt -l")
	}
	for rel, banned := range map[string][]string{
		"cmd/proveo-dev/hooks.go":      {`"commit"`, `"config"`, `"add"`, `"--amend"`},
		"scripts/githooks/post-commit": {"git commit", "git config", "git add", "--amend"},
	} {
		body := readRepoFile(t, rel)
		for _, b := range banned {
			if strings.Contains(body, b) {
				t.Errorf("%s contains %q — that loops or mutates git config", rel, b)
			}
		}
	}
	if !strings.Contains(src, "SKIP_GIT_HOOKS") {
		t.Error("the post-commit gate must honour SKIP_GIT_HOOKS so CI can skip it")
	}
}

// The hook git executes stays a file, so it is POSIX sh that execs Go:
// /bin/sh on macOS is bash 3.2 in posix mode.
func TestPostCommitShimIsPOSIXAndExecsProveoDev(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "scripts/githooks/post-commit")
	if !strings.HasPrefix(src, "#!/bin/sh\n") {
		t.Errorf("post-commit must start with #!/bin/sh, got %q", strings.SplitN(src, "\n", 2)[0])
	}
	if !strings.Contains(src, "go run ./cmd/proveo-dev hooks post-commit") {
		t.Error("post-commit must exec proveo-dev hooks post-commit")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh unavailable")
	}
	if out, err := exec.Command(sh, "-n", filepath.Join(repoRoot(t), "scripts", "githooks", "post-commit")).CombinedOutput(); err != nil {
		t.Errorf("post-commit does not parse under sh: %v\n%s", err, out)
	}
}

func TestInstallGitHooksCopiesPostCommitWithoutGitConfig(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "cmd/proveo-dev/hooks.go")
	for _, want := range []string{"scripts/githooks/post-commit", ".git/hooks/post-commit", "0o755"} {
		if !strings.Contains(src, want) {
			t.Errorf("proveo-dev hooks install lacks %q", want)
		}
	}
	if strings.Contains(src, "hooksPath") {
		t.Error("install must not set core.hooksPath")
	}
}

func TestMiseDeclaresInstallGitHooks(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "mise.toml")
	if !strings.Contains(src, "[tasks.install-git-hooks]") {
		t.Fatal("mise.toml must declare install-git-hooks")
	}
	if !strings.Contains(src, "go run ./cmd/proveo-dev hooks install") {
		t.Error("install-git-hooks must run proveo-dev hooks install")
	}
}

func TestInstallGitHooksWritesAnExecutablePostCommit(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	bin := filepath.Join(t.TempDir(), "proveo-dev")
	build := exec.Command("go", "build", "-o", bin, "./cmd/proveo-dev")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build proveo-dev: %v\n%s", err, out)
	}
	root := t.TempDir()
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "githooks", "post-commit"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "scripts", "githooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "githooks", "post-commit"), body, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "hooks", "install")
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
