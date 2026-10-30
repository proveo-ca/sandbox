// SPEC: _spec/_paradigms/credential-boundary.puml
package run

import (
	"slices"
	"testing"
)

func TestStateRootUsesItsConfiguredName(t *testing.T) {
	t.Setenv("PROVEO_STATE_ROOT", "/state")
	t.Setenv("PROVEO_EGRESS_ROOT", "/old-state")
	if got := StateDir(); got != "/state" {
		t.Errorf("StateDir() = %q, want PROVEO_STATE_ROOT", got)
	}
	t.Setenv("PROVEO_STATE_ROOT", "")
	t.Setenv("XDG_STATE_HOME", "/xdg-state")
	if got := StateDir(); got != "/xdg-state/proveo" {
		t.Errorf("StateDir() = %q, want XDG_STATE_HOME fallback", got)
	}
}

func TestRunStateLivesUnderRuns(t *testing.T) {
	t.Setenv("PROVEO_STATE_ROOT", "/state")
	if got := RunDirOf("proveo-1-2"); got != "/state/runs/proveo-1-2" {
		t.Errorf("RunDirOf = %q, want it under runs/", got)
	}
	if got, want := RunRoots(), []string{"/state/runs"}; !slices.Equal(got, want) {
		t.Errorf("RunRoots() = %v, want %v", got, want)
	}
}
