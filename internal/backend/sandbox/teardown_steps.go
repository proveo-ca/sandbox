// SPEC: _spec/internal/backend/teardown.puml
package sandbox

import (
	"fmt"
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

// lastStep is the step that most recently began.
var lastStep atomic.Pointer[string]

// runningStep names the step that most recently began.
func runningStep() string {
	if p := lastStep.Load(); p != nil {
		return *p
	}
	return "its first step"
}

// withCeiling is name followed by its ceiling, when it has one.
func withCeiling(name string) string {
	if d := stepCeiling(name); d > 0 {
		return name + " (up to " + d.Round(time.Second).String() + ")"
	}
	return name
}

// stepPlace is where the running top-level step sits in teardown's plan.
type stepPlace struct{ n, of int }

// placeNow is the running top-level step's place; nil outside teardown.
var placeNow atomic.Pointer[stepPlace]

// stepClock is how often a step's live line redraws its elapsed time.
var stepClock = time.Second

// stepFrame is a step's live line: its place in the plan, its name, how long it has run, and its ceiling.
func stepFrame(name string, ran time.Duration) string {
	frame := name
	if p := placeNow.Load(); p != nil {
		frame = fmt.Sprintf("step %d/%d · %s", p.n, p.of, name)
	}
	frame += " · " + ran.Round(time.Second).String()
	if d := stepCeiling(name); d > 0 {
		frame += " of up to " + d.Round(time.Second).String()
	}
	return frame
}

// showStep says which step the terminal is about to wait on and returns the func that
// ends it. A terminal gets one live line with a running clock; any other output gets
// a start line. The run log gets the start either way.
func showStep(name string) (done func()) {
	lastStep.Store(&name)
	ui.Logf("step: %s started", withCeiling(name))
	if !ui.Fancy() {
		ui.Asyncf("%s", startLine(name))
		return func() {}
	}
	began := time.Now()
	ui.Live(ui.RoleAsync, "%s", stepFrame(name, 0))
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(stepClock)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				ui.Live(ui.RoleAsync, "%s", stepFrame(name, time.Since(began)))
			case <-stop:
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-stopped
		ui.EndLive()
	}
}

// startLine is a step's start on plain output: its name, then its ceiling when it has one.
func startLine(name string) string {
	if d := stepCeiling(name); d > 0 {
		return name + " — up to " + d.Round(time.Second).String()
	}
	return name
}

// shown is step behind its live line.
func shown(name string, fn func() error) error {
	defer showStep(name)()
	return step(name, fn)
}

// shownDo is stepDo behind its live line.
func shownDo(name string, fn func()) {
	defer showStep(name)()
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
