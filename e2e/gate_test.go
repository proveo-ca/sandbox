//go:build e2e

// SPEC: _spec/_devops/release-gate.puml
package e2e

import (
	"fmt"
	"os"
	"testing"

	"github.com/proveo-ca/proveo/internal/envflag"
)

// failSkipsWhenOptedIn fails t at cleanup when t skipped while the optIn switch is on.
func failSkipsWhenOptedIn(t testing.TB, optIn string) {
	t.Helper()
	if !envflag.On(os.Getenv(optIn)) {
		return
	}
	t.Cleanup(func() {
		if t.Skipped() {
			t.Errorf("%s is on, so this skip fails the gate", optIn)
		}
	})
}

// skipRecorder records what failSkipsWhenOptedIn does to a test without running one.
type skipRecorder struct {
	testing.TB
	skipped  bool
	errs     []string
	cleanups []func()
}

func (r *skipRecorder) Helper()          {}
func (r *skipRecorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }
func (r *skipRecorder) Skipped() bool    { return r.skipped }
func (r *skipRecorder) Errorf(format string, a ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, a...))
}

func TestOptInTurnsASkipIntoAFailure(t *testing.T) {
	const optIn = "E2E_GATE_PROBE"
	for _, c := range []struct {
		value     string
		skipped   bool
		cleanups  int
		wantError bool
	}{
		{"1", true, 1, true},
		{"1", false, 1, false},
		{"", true, 0, false},
		{"0", true, 0, false},
	} {
		t.Setenv(optIn, c.value)
		r := &skipRecorder{skipped: c.skipped}
		failSkipsWhenOptedIn(r, optIn)
		if len(r.cleanups) != c.cleanups {
			t.Errorf("%s=%q: %d cleanups registered, want %d", optIn, c.value, len(r.cleanups), c.cleanups)
		}
		for _, f := range r.cleanups {
			f()
		}
		if got := len(r.errs) > 0; got != c.wantError {
			t.Errorf("%s=%q skipped=%v: failed=%v, want %v (%q)", optIn, c.value, c.skipped, got, c.wantError, r.errs)
		}
		if c.wantError && (len(r.errs) != 1 || r.errs[0] != optIn+" is on, so this skip fails the gate") {
			t.Errorf("%s=%q: errors %q, want one naming the switch", optIn, c.value, r.errs)
		}
	}
}
