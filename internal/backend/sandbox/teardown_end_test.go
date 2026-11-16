// SPEC: _spec/internal/sbx/kit-lifecycle.puml
package sandbox

import (
	"errors"
	"syscall"
	"testing"
)

func TestAForwardedStopTearsDownLikeANormalExitAndReportsTheSignal(t *testing.T) {
	failed := errors.New("context canceled")
	stopped := Stopped{Signal: syscall.SIGTERM}
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

// SPEC: _spec/internal/sbx/state-sync.puml
func TestANormalEndRemovesTheSandboxUnlessWorkStateOrTheEngineStaysInIt(t *testing.T) {
	stranded := errors.New("the agent's branches are still only in sb")
	unsaved := errors.New("exit status 1: proveo: state save refused")
	for _, c := range []struct {
		name          string
		forced        bool
		stranded      error
		unsaved       error
		retainsEngine bool
		want          normalEnd
	}{
		{"a clean exit", false, nil, nil, false, endRemove},
		{"work still only in the sandbox", false, stranded, nil, false, endKeepStranded},
		{"resume state that did not reach the host", false, nil, unsaved, false, endKeepUnsaved},
		{"stranded work and unsaved state", false, stranded, unsaved, false, endKeepStranded},
		{"the OpenCode engine", false, nil, nil, true, endRetainEngine},
		{"unsaved state in the OpenCode engine", false, nil, unsaved, true, endKeepUnsaved},
		{"stranded work in the OpenCode engine", false, stranded, nil, true, endKeepStranded},
		{"the operator chose remove", true, stranded, nil, true, endRemove},
		{"the operator chose remove over unsaved state", true, nil, unsaved, false, endRemove},
	} {
		if got := normalEndOf(c.forced, c.stranded, c.unsaved, c.retainsEngine); got != c.want {
			t.Errorf("normalEndOf(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
