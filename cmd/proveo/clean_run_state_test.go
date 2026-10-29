// SPEC: _spec/internal/clean/clean-lifecycle.puml
package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/proveo-ca/proveo/internal/clean"
)

func TestCleanSweepsRunStateUnderRunsOnly(t *testing.T) {
	state := t.TempDir()
	t.Setenv("PROVEO_STATE_ROOT", state)
	dirs := []string{"runs/proveo-1-1", "runs/proveo-3-3"}
	retired := []string{"egress/proveo-2-2", "egress/proveo-1-1"}
	for _, d := range retired {
		if err := os.MkdirAll(filepath.Join(state, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(state, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := stateSessions(), []string{"proveo-1-1", "proveo-3-3"}; !slices.Equal(got, want) {
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
	for _, d := range retired {
		if _, err := os.Stat(filepath.Join(state, d)); err != nil {
			t.Errorf("unmanaged state %s was removed (%v)", d, err)
		}
	}
}
