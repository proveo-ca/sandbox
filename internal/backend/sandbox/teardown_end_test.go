// SPEC: _spec/internal/sbx/kit-lifecycle.puml
package sandbox

import (
	"errors"
	"syscall"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend"
)

func TestAForwardedStopTearsDownLikeANormalExitAndReportsTheSignal(t *testing.T) {
	failed := errors.New("context canceled")
	stopped := backend.Stopped{Signal: syscall.SIGTERM}
	for _, c := range []struct {
		name         string
		sig          syscall.Signal
		forced       bool
		wantTeardown error
		wantResult   error
	}{
		{"stopped by SIGTERM", syscall.SIGTERM, false, nil, stopped},
		{"the Ctrl+C remove path", 0, true, failed, failed},
		{"a signal after the operator chose remove", syscall.SIGTERM, true, failed, failed},
		{"an agent that failed on its own", 0, false, failed, failed},
	} {
		teardown, result := stopOutcome(failed, c.sig, c.forced)
		if teardown != c.wantTeardown || result != c.wantResult {
			t.Errorf("%s: stopOutcome = (%v, %v), want (%v, %v)", c.name, teardown, result, c.wantTeardown, c.wantResult)
		}
	}
}

func TestANormalEndRemovesTheSandboxUnlessWorkOrTheEngineStaysInIt(t *testing.T) {
	stranded := errors.New("the agent's branches are still only in sb")
	for _, c := range []struct {
		name          string
		forced        bool
		stranded      error
		retainsEngine bool
		want          normalEnd
	}{
		{"a clean exit", false, nil, false, endRemove},
		{"work still only in the sandbox", false, stranded, false, endKeepStranded},
		{"the OpenCode engine", false, nil, true, endRetainEngine},
		{"stranded work in the OpenCode engine", false, stranded, true, endKeepStranded},
		{"the operator chose remove", true, stranded, true, endRemove},
	} {
		if got := normalEndOf(c.forced, c.stranded, c.retainsEngine); got != c.want {
			t.Errorf("normalEndOf(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
