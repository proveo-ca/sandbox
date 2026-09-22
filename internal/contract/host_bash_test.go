// SPEC: _spec/_plans/host-shell-to-go.puml
package contract_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// hostShellAllowed is every tracked shell file the operator's Mac may execute.
var hostShellAllowed = map[string]string{
	"apps/cli/public/cli/install.sh":   "bootstraps proveo before it exists; guarded by installer_bash_test.go",
	"apps/cli/public/cli/uninstall.sh": "removes proveo; guarded by installer_bash_test.go",
	"scripts/githooks/post-commit":     "git needs an executable hook file; POSIX sh that execs proveo-dev",
}

// inImageOrFixture are trees whose shell runs inside an image (bash 5) or is a
// fixture workspace an agent operates on, never the host.
var inImageOrFixture = []string{"defs/", "packages/lib/", "e2e/testdata/", "e2e/samples/"}

var shellShebang = regexp.MustCompile(`^#!\S*(/|env\s+)(ba|da|z)?sh(\s|$)`)

func TestNoHostShellOutsideTheAllowlist(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Skipf("git ls-files: %v", err)
	}
	seen := map[string]bool{}
	for _, rel := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if rel == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		first, _, _ := bytes.Cut(b, []byte("\n"))
		if !strings.HasSuffix(rel, ".sh") && !shellShebang.Match(first) {
			continue
		}
		if _, ok := hostShellAllowed[rel]; ok {
			seen[rel] = true
			continue
		}
		inside := false
		for _, prefix := range inImageOrFixture {
			inside = inside || strings.HasPrefix(rel, prefix)
		}
		if !inside {
			t.Errorf("%s is a host shell script; macOS runs it under /bin/bash 3.2. "+
				"Port it to cmd/proveo-dev (maintainer tooling) or cmd/proveo, "+
				"or justify it in hostShellAllowed", rel)
		}
	}
	for rel := range hostShellAllowed {
		if !seen[rel] {
			t.Errorf("hostShellAllowed lists %s, which is not a tracked shell file — drop the entry", rel)
		}
	}
}

func TestMiseTasksRunNoShellScripts(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "mise.toml")
	for _, banned := range []string{"#!/usr/bin/env bash", "#!/bin/bash", "bash scripts/", "run = \"\"\""} {
		if strings.Contains(src, banned) {
			t.Errorf("mise.toml contains %q — a task body is shell again; make it one command into cmd/proveo-dev", banned)
		}
	}
}
