// SPEC: _spec/_plans/config-seeding-and-persistence.puml
package main

import (
	"os"
	"path/filepath"
	"testing"
)

// `--tools` has to reclaim BOTH shapes: the legacy tree the previous version
// installed straight into PROVEO_HOME, and every platform-namespaced tree the
// relocation creates. Missing the first strands whatever an operator already
// has; missing the second makes the flag silently reclaim nothing on a host
// that only ever ran sbx.
func TestToolRootsCoversLegacyAndEveryPlatform(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, d := range []string{
		"toolchains/linux-arm64",
		"toolchains/linux-amd64",
	} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A stray file under toolchains/ is not a root.
	if err := os.WriteFile(filepath.Join(root, "toolchains", "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := toolRoots(root)
	want := []string{
		root,
		filepath.Join(root, "toolchains", "linux-amd64"),
		filepath.Join(root, "toolchains", "linux-arm64"),
	}
	if len(got) != len(want) {
		t.Fatalf("toolRoots() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("toolRoots()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// A host that has never run the relocated seed still has exactly one root.
func TestToolRootsWithNoToolchainsDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if got := toolRoots(root); len(got) != 1 || got[0] != root {
		t.Errorf("toolRoots() = %v, want just the legacy root %q", got, root)
	}
}
