package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A subproject scope is handed to the sandbox as sibling workspaces — the
// scope, .git, _spec — each mounted at its host path. If the scope arrives as
// typed (/var/...) and its siblings arrive canonical (/private/var/...), the VM
// sees unrelated trees and git finds no repository from the scope.
func TestMountsShareOneCanonicalRootForASubprojectScope(t *testing.T) {
	real := t.TempDir()
	for _, d := range []string{"apps/web", "_spec", ".git"} {
		if err := os.MkdirAll(filepath.Join(real, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(t.TempDir(), "via-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}

	// Typed through the symlink, repo root as git would report it.
	w := MountSpec{InputDir: filepath.Join(link, "apps"), RepoRoot: canonical}
	mounts, _, _ := w.Plan()
	if len(mounts) < 2 {
		t.Fatalf("expected the scope and its .git at least, got %v", mounts)
	}
	for _, m := range mounts {
		if !strings.HasPrefix(m.Host, canonical+string(os.PathSeparator)) && m.Host != canonical {
			t.Errorf("mount host %q is not under the canonical root %q — the sandbox "+
				"would place it in a different tree from its siblings", m.Host, canonical)
		}
	}
}
