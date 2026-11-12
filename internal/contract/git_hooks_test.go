// SPEC: _spec/_devops/git-hooks.puml
package contract_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/envflag"
)

func TestPostCommitRunsFormatThenLint(t *testing.T) {
	t.Parallel()
	task := readMise(t).task(t, "post-commit")
	if task.str("hide") != "true" {
		t.Error("[tasks.post-commit] must set hide = true: git runs it, an operator never does")
	}
	var got []string
	for _, r := range task.run() {
		got = append(got, r.String())
	}
	if want := []string{"{ task = fmt }", "{ task = lint }"}; !slices.Equal(got, want) {
		t.Errorf("post-commit runs %q, want %q: format first, because lint's first gate is gofmt", got, want)
	}
	if cmds := task.cmds(); len(cmds) != 0 {
		t.Errorf("post-commit runs the string entries %q; its body is the fmt and lint tasks alone", cmds)
	}
	for _, banned := range []string{"git add", "git commit", "git config", "--amend"} {
		if strings.Contains(task.code(), banned) {
			t.Errorf("[tasks.post-commit] names %q — that loops or mutates git config", banned)
		}
	}
	if !strings.Contains(task.str("skip_if"), "SKIP_GIT_HOOKS") {
		t.Error("post-commit's skip_if must read SKIP_GIT_HOOKS so CI can skip the hook")
	}
}

var skipIfWords = regexp.MustCompile(`\bin \[([^\]]*)\]`)

func TestPostCommitSkipIfSpeaksTheEnvflagWords(t *testing.T) {
	t.Parallel()
	expr := readMise(t).task(t, "post-commit").str("skip_if")
	var want []string
	for w := range envSwitchWords {
		if envflag.On(w) {
			want = append(want, w)
		}
	}
	slices.Sort(want)
	m := skipIfWords.FindStringSubmatch(expr)
	if m == nil {
		t.Fatalf("post-commit's skip_if %q tests no `in [...]` word list", expr)
	}
	var got []string
	for _, w := range strings.Split(m[1], ",") {
		got = append(got, strings.Trim(strings.TrimSpace(w), `'"`))
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("post-commit's skip_if skips on %q; envflag.On accepts %q", got, want)
	}
	for _, part := range []string{"get_env(name='SKIP_GIT_HOOKS', default='')", "| trim", "| lower", "%}true{% else %}false{% endif %}"} {
		if !strings.Contains(expr, part) {
			t.Errorf("post-commit's skip_if lacks %q; envflag.On trims and lowercases the value", part)
		}
	}
}

func TestMiseDeclaresInstallGitHooks(t *testing.T) {
	t.Parallel()
	task := readMise(t).task(t, "install-git-hooks")
	want := []string{"mise generate git-pre-commit --hook post-commit --task post-commit --write"}
	if got := task.cmds(); !slices.Equal(got, want) || len(task.run()) != 1 {
		t.Errorf("install-git-hooks runs %q, want %q: mise writes the hook into git's hooks dir", got, want)
	}
	for _, banned := range []string{"git config", "hooksPath"} {
		if strings.Contains(task.code(), banned) {
			t.Errorf("[tasks.install-git-hooks] names %q; installing the hook must set no git config", banned)
		}
	}
	if !strings.Contains(readRepoFile(t, "CONTRIBUTING.md"), "`mise run install-git-hooks`") {
		t.Error("CONTRIBUTING.md never names `mise run install-git-hooks`; a clone has no hook, " +
			"and one installed before 2026-11-12 fails after every commit until the task runs")
	}
}
