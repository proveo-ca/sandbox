// SPEC: _spec/_devops/git-hooks.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkipHooksAcceptsTheBashTruthyWords(t *testing.T) {
	for _, v := range []string{"1", "true", "yes", "on"} {
		if !skipHooks(v) {
			t.Errorf("skipHooks(%q) = false", v)
		}
	}
	for _, v := range []string{"", "0", "false", "TRUE", "no"} {
		if skipHooks(v) {
			t.Errorf("skipHooks(%q) = true", v)
		}
	}
}

func TestHookPathPrependsLocalBins(t *testing.T) {
	got := hookPath("/h", "/usr/bin")
	if want := "/usr/local/bin:/h/.local/bin:/usr/bin"; got != want {
		t.Errorf("hookPath = %q, want %q", got, want)
	}
}

func TestInstallHookCopiesTheVersionedShimExecutable(t *testing.T) {
	top := t.TempDir()
	body := []byte("#!/bin/sh\nexec true\n")
	if err := os.MkdirAll(filepath.Join(top, "scripts", "githooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(top, hookSrc), body, 0o644); err != nil {
		t.Fatal(err)
	}
	dst, err := installHook(top)
	if err != nil {
		t.Fatal(err)
	}
	if dst != filepath.Join(top, ".git", "hooks", "post-commit") {
		t.Errorf("dst = %s", dst)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 0755", st.Mode().Perm())
	}
	got, _ := os.ReadFile(dst)
	if string(got) != string(body) {
		t.Error("installed hook is not the versioned script")
	}
}

func TestInstallHookRefusesAMissingOrEmptySource(t *testing.T) {
	top := t.TempDir()
	if _, err := installHook(top); err == nil || !strings.HasPrefix(err.Error(), "missing ") {
		t.Errorf("missing source: err = %v", err)
	}
	_ = os.MkdirAll(filepath.Join(top, "scripts", "githooks"), 0o755)
	_ = os.WriteFile(filepath.Join(top, hookSrc), nil, 0o644)
	if _, err := installHook(top); err == nil {
		t.Error("empty source installed")
	}
}

func TestHookInstallSetsNoGitConfig(t *testing.T) {
	src, err := os.ReadFile("hooks.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{`"config"`, "hooksPath", `"commit"`, `"add"`, "--amend"} {
		if strings.Contains(string(src), banned) {
			t.Errorf("hooks.go contains %s: the hook must not mutate git config or loop commits", banned)
		}
	}
	if fmtAt, lintAt := strings.Index(string(src), `"run", "fmt"`), strings.Index(string(src), `"run", "lint"`); fmtAt < 0 || lintAt < 0 || fmtAt > lintAt {
		t.Error("post-commit must run mise run fmt before mise run lint")
	}
}
