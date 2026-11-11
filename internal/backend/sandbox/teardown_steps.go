// SPEC: _spec/internal/backend/teardown.puml
package sandbox

import (
	"strings"
	"sync/atomic"
	"time"

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
