// SPEC: _spec/internal/backend/teardown.puml
package sandbox

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// Step names, as the run log shows them.
const (
	stepAgent         = "agent"
	stepStopWait      = "stop wait"
	stepCredential    = "credential check"
	stepCloneMode     = "clone mode"
	stepPushed        = "pushed branches"
	stepEgress        = "egress record"
	stepMemory        = "memory evidence"
	stepClone         = "clone"
	stepCloneEnv      = "clone/.env"
	stepCloneSnapshot = "clone/snapshot"
	stepCloneOutput   = "clone/output"
	stepCloneCarry    = "clone/branches"
	stepCloneSpec     = "clone/spec"
	stepState         = "resume state"
	stepHome          = "home config"
	stepRemove        = "remove sandbox"
)

// stepCeiling is the longest the named step waits on its own subprocesses; 0 means it starts none.
func stepCeiling(name string) time.Duration {
	call := guestExecDeadline + guestGrace
	switch name {
	case stepCredential:
		return sbx.ListWait + sbx.ExecWait(credentialProbeWait)
	case stepCloneMode:
		return cloneProbeDeadline + guestGrace
	case stepPushed, stepCloneEnv, stepState:
		return sbx.ListWait + call
	case stepEgress:
		return sbx.ListWait
	case stepMemory:
		return 2 * sbx.ListWait
	case stepCloneSnapshot, stepCloneOutput, stepRemove:
		return call
	case stepCloneCarry:
		return sbx.ListWait + 4*call
	case stepCloneSpec:
		return 7 * call
	}
	return 0
}

// announce prints the step the terminal is about to wait on and its ceiling.
func announce(name string) {
	if d := stepCeiling(name); d > 0 {
		ui.Asyncf("%s — up to %s", name, d.Round(time.Second))
		return
	}
	ui.Asyncf("%s", name)
}

// shown is step behind its start line.
func shown(name string, fn func() error) error {
	announce(name)
	return step(name, fn)
}

// shownDo is stepDo behind its start line.
func shownDo(name string, fn func()) {
	announce(name)
	stepDo(name, fn)
}

// stepSink receives each finished step; nil sends it to the run log.
var stepSink atomic.Pointer[func(name string, took time.Duration, err error)]

// step runs fn as the named step and records its duration and outcome.
func step(name string, fn func() error) error {
	began := time.Now()
	err := fn()
	recordStep(name, time.Since(began), err)
	return err
}

// stepDo is step for a call that reports no error.
func stepDo(name string, fn func()) {
	_ = step(name, func() error { fn(); return nil })
}

func recordStep(name string, took time.Duration, err error) {
	if sink := stepSink.Load(); sink != nil {
		(*sink)(name, took, err)
		return
	}
	outcome := "ok"
	if err != nil {
		outcome = "failed: " + strings.SplitN(err.Error(), "\n", 2)[0]
	}
	ui.Logf("step: %s %s %s", name, took.Round(time.Millisecond), outcome)
}
