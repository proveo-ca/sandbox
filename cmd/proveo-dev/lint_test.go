// SPEC: _spec/_plans/host-shell-to-go.puml
package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestGofmtTargetsCoverExistingDirsAndRootGoFiles(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"internal", "cmd"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"embed.go", "README.md"} {
		if err := os.WriteFile(filepath.Join(root, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := gofmtTargets(root)
	if want := []string{"internal", "cmd", "embed.go"}; !slices.Equal(got, want) {
		t.Errorf("gofmtTargets = %v, want %v", got, want)
	}
}

func TestUnformattedListsOnlyBadFiles(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "internal"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "internal", "ok.go"), []byte("package x\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "bad.go"), []byte("package x\nfunc  f( ) {}\n"), 0o644)
	if got := unformatted(root); !slices.Equal(got, []string{"bad.go"}) {
		t.Errorf("unformatted = %v, want [bad.go]", got)
	}
}

func TestSplitLinesDropsBlanks(t *testing.T) {
	if got := splitLines("a\n\n b \n"); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("splitLines = %v", got)
	}
}
