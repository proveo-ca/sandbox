// SPEC: _spec/internal/clean/clean-lifecycle.puml
package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/proveo-ca/proveo/internal/clean"
)

func TestCleanSweepsRunStateUnderTheRetiredRootToo(t *testing.T) {
	state := t.TempDir()
	t.Setenv("PROVEO_EGRESS_ROOT", "")
	t.Setenv("PROVEO_STATE_ROOT", state)
	dirs := []string{"runs/proveo-1-1", "egress/proveo-2-2", "runs/proveo-3-3", "egress/proveo-3-3"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(state, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := stateSessions(), []string{"proveo-1-1", "proveo-2-2", "proveo-3-3"}; !slices.Equal(got, want) {
		t.Errorf("stateSessions() = %v, want %v", got, want)
	}
	if err := runClean(clean.Plan{StateDirs: stateSessions()}, false); err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(state, d)); !os.IsNotExist(err) {
			t.Errorf("%s survived clean (%v)", d, err)
		}
	}
}
