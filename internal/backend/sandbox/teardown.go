// SPEC: _spec/internal/backend/teardown.puml
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// teardown is what runs once the agent has exited.
type teardown struct {
	in      Input
	cfg     sbx.RunConfig
	secrets [][2]string

	failed bool // the agent's run ended in an error the operator did not force
	forced bool // the operator chose remove at the Ctrl+C dialog

	started, ended time.Time
	lastOutput     func() []string
	commitHome     func() error

	keepHome bool // a sandbox outlives the run, so its home access does too

	interrupts atomic.Int32           // SIGINTs since teardown began
	asked      atomic.Int32           // the signal that asked teardown to leave; 0 for none
	askedAt    atomic.Pointer[string] // the step running when it asked
	began      []string               // the top-level steps that started
	carry      error                  // the clone step's outcome
	left       bool                   // teardown stopped with part of its plan undone
	kept       string                 // why the sandbox outlives the run; "" when it does not
}

// errStepSkipped is a step's outcome once teardown's context has ended.
var errStepSkipped = errors.New("skipped: teardown was stopped")

// teardownPlan lists an ending's top-level steps in the order they run.
func teardownPlan(failed bool) []string {
	plan := []string{stepCredential, stepCloneMode, stepPushed, stepEgress, stepMemory, stepClone, stepState, stepHome}
	if !failed {
		plan = append(plan, stepRemove)
	}
	return plan
}

// do runs one top-level step behind its start line; it skips the step once ctx
// ended, and a panic becomes the step's error.
func (t *teardown) do(ctx context.Context, name string, fn func() error) error {
	if t.leaving(ctx) {
		recordStep(name, 0, errStepSkipped)
		return errStepSkipped
	}
	t.began = append(t.began, name)
	lastStep.Store(&name)
	plan := teardownPlan(t.failed)
	placeNow.Store(&stepPlace{n: slices.Index(plan, name) + 1, of: len(plan)})
	defer placeNow.Store(nil)
	if name != stepClone {
		defer showStep(name)()
	}
	return step(name, func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		return fn()
	})
}

func (t *teardown) run(ctx context.Context) {
	t.in.Stop = ctx
	cfg := t.cfg
	recordKept(t.in, cfg.Name, "its teardown did not finish", true)
	defer func() { recordKept(t.in, cfg.Name, t.kept, false) }()
	ui.Section(ui.SectionResults)
	_ = t.do(ctx, stepCredential, func() error { reportCredentialInUse(t.in.Target, cfg.Name); return nil })
	_ = t.do(ctx, stepCloneMode, func() error {
		clones, err := sandboxClone(t.in, cfg.Name)
		if err == nil {
			t.in.Clone, t.in.CloneKnown = clones, true
		}
		return err
	})
	_ = t.do(ctx, stepPushed, func() error { reportPushedBranches(t.in, cfg, t.started); return nil })
	_ = t.do(ctx, stepEgress, func() error { CapturePolicyLog(t.in.RunDir, cfg.Name); return nil })
	_ = t.do(ctx, stepMemory, func() error { CaptureMemoryEvidence(t.in.RunDir, cfg.Name); return nil })
	if t.failed {
		t.failedEnd(ctx)
		return
	}
	t.normalEnd(ctx)
}

// watch routes SIGINT, SIGTERM and SIGHUP to teardown until end is called. The
// context it returns ends when the operator leaves.
func (t *teardown) watch(parent context.Context) (ctx context.Context, end func()) {
	ctx, cancel := context.WithCancel(parent)
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				if sig, ok := s.(syscall.Signal); ok {
					t.signalled(sig, cancel)
				}
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			signal.Stop(sigs)
			close(done)
			cancel()
		})
	}
}

// signalled applies one signal. SIGTERM and SIGHUP leave once the running step
// ends; the first SIGINT says what leaving keeps; the next one leaves now.
func (t *teardown) signalled(sig syscall.Signal, leaveNow context.CancelFunc) {
	at := runningStep()
	switch {
	case sig != syscall.SIGINT:
		if t.asked.CompareAndSwap(0, int32(sig)) {
			t.askedAt.Store(&at)
			ui.Warnf("%v — leaving once %s finishes; sandbox %s stays", backend.Stopped{Signal: sig}, at, t.cfg.Name)
		}
	case t.interrupts.Add(1) == 1 && t.asked.Load() == 0:
		ui.Warnf("teardown is at %s — Ctrl+C again leaves now", withCeiling(at))
		ui.Notef("leaving keeps sandbox %s; work it has not carried home stays only there", t.cfg.Name)
	default:
		t.asked.Store(int32(syscall.SIGINT))
		t.askedAt.Store(&at)
		leaveNow()
	}
}

// leaving reports whether a signal or an ended context asked teardown to stop.
func (t *teardown) leaving(ctx context.Context) bool {
	return ctx.Err() != nil || t.asked.Load() != 0
}

// notRun lists the planned steps that never started.
func (t *teardown) notRun() []string {
	var rest []string
	for _, name := range teardownPlan(t.failed) {
		if !slices.Contains(t.began, name) {
			rest = append(rest, name)
		}
	}
	return rest
}

// mustLeave reports whether a stop left part of the plan undone.
func (t *teardown) mustLeave(ctx context.Context) bool {
	return t.leaving(ctx) && (ctx.Err() != nil || len(t.notRun()) > 0)
}

// leftBy is the signal that made teardown leave part of its plan undone, or 0.
func (t *teardown) leftBy() syscall.Signal {
	if !t.left {
		return 0
	}
	return syscall.Signal(t.asked.Load())
}

// leave says what a stopped teardown left in place: the sandbox, the steps not run, and the way back in.
func (t *teardown) leave() {
	reason := "teardown was stopped"
	if sig := syscall.Signal(t.asked.Load()); sig != 0 {
		reason = "teardown " + backend.Stopped{Signal: sig}.Error()
	}
	if at := t.askedAt.Load(); at != nil {
		reason += " at " + *at
	}
	why := "(" + reason + ")"
	if t.failed {
		why, reason = "for diagnosis (the run failed) "+why, "the run failed; "+reason
	}
	ui.Section(ui.SectionResults)
	lines := keptBecause(t.cfg.Name, why, t.in.RunLog)
	ui.Warnf("%s", lines[0])
	if rest := t.notRun(); len(rest) > 0 {
		ui.Notef("not run: %s", strings.Join(rest, ", "))
	}
	if errors.Is(t.carry, errStepSkipped) && (t.in.Clone || !t.in.CloneKnown) {
		for _, l := range CloneRescueLines(t.cfg.Name, t.in.Sid, agentWorkdir(t.in, t.cfg.Mounts), t.in.RepoRoot) {
			ui.Notef("%s", l)
		}
	}
	for _, l := range lines[1:] {
		ui.Notef("%s", l)
	}
	t.keepHome, t.left, t.kept = true, true, reason
}

// saveState is SaveState under ctx.
func (t *teardown) saveState(ctx context.Context) (string, error) {
	return SaveState(t.cfg.Name, t.cfg.Env, sbx.Exists(t.cfg.Name), func(args ...string) (string, error) {
		return guestRunIn(ctx, args...)
	})
}

// failedEnd carries what it can home and keeps the sandbox for diagnosis.
func (t *teardown) failedEnd(ctx context.Context) {
	in, cfg := t.in, t.cfg
	said := false
	if lines := t.lastOutput(); len(lines) > 0 {
		said = true
		ui.Section(ui.SectionResults)
		ui.Hostf("last output from the agent:")
		for _, l := range lines {
			ui.Notef("%s", l)
		}
	}
	restarted := !sbx.Running(cfg.Name)
	t.carry = t.do(ctx, stepClone, func() error { return TeardownClone(in, cfg) })
	_ = t.do(ctx, stepState, func() error {
		_, err := t.saveState(ctx)
		return err
	})
	if err := t.do(ctx, stepHome, t.commitHome); err != nil && !errors.Is(err, errStepSkipped) {
		ui.Warnf("home-root config not preserved: %v", err)
	}
	if t.mustLeave(ctx) {
		t.leave()
		return
	}
	if tr := credentials.AgentTranscript(in.Target, in.HomeRoot, t.started, t.ended); tr != "" {
		said = true
		ui.Storef("what the agent actually said is in %s", tr)
	} else if restarted && sbx.Exists(cfg.Name) {
		ui.Storef("no transcript from this run — the sandbox had already stopped, " +
			"so state was copied out after it ended and anything newer than the run is the harvest's own")
	}
	if !said {
		if hint := credentials.NoCredentialHint(in.Man, in.Target, in.HomeRoot, cfg.Env, t.secrets,
			sbx.StoredServices(), in.Lookup); len(hint) > 0 {
			ui.Hostf("%s", hint[0])
			for _, l := range hint[1:] {
				ui.Notef("%s", l)
			}
		}
	}
	ui.Section(ui.SectionResults)
	kept := KeptLines(cfg.Name, in.RunLog)
	ui.Warnf("%s", kept[0])
	for _, l := range kept[1:] {
		ui.Notef("%s", l)
	}
	if sbx.Exists(cfg.Name) {
		t.keepHome, t.kept = true, "the run failed"
		PrintIDEAttach(in, cfg, false)
	}
}

// normalEnd carries the work home, then removes the sandbox unless work or history stays in it.
func (t *teardown) normalEnd(ctx context.Context) {
	in, cfg := t.in, t.cfg
	stranded := t.do(ctx, stepClone, func() error { return TeardownClone(in, cfg) })
	t.carry = stranded
	var saved string
	if err := t.do(ctx, stepState, func() (err error) {
		saved, err = t.saveState(ctx)
		return err
	}); err != nil && !errors.Is(err, errStepSkipped) {
		ui.Warnf("resume state not preserved (%v): %s", err, strings.TrimSpace(saved))
	}
	if err := t.do(ctx, stepHome, t.commitHome); err != nil && !errors.Is(err, errStepSkipped) {
		ui.Warnf("home-root config not preserved: %v", err)
	}
	if t.mustLeave(ctx) {
		t.leave()
		return
	}
	switch normalEndOf(t.forced, stranded, opencodeTarget(in.Target)) {
	case endKeepStranded:
		t.keepHome, t.kept = true, stranded.Error()
		if opencodeTarget(in.Target) {
			ui.Warnf("%v — %s kept with its OpenCode history", stranded, cfg.Name)
		} else {
			ui.Warnf("%v — %s kept; `proveo rm %s` carries the work home and removes it", stranded, cfg.Name, cfg.Name)
		}
		PrintIDEAttach(in, cfg, false)
		return
	case endRetainEngine:
		t.keepHome = true
		ui.Storef("OpenCode: history lives in retained engine %s; `sbx rm --force %s` deletes it", cfg.Name, cfg.Name)
		return
	}
	if t.forced {
		ui.Hostf("sbx rm --force %s — deleting the session", cfg.Name)
	}
	if t.do(ctx, stepRemove, func() error { return removeSandbox(ctx, cfg.Name) }) != nil {
		if t.mustLeave(ctx) {
			t.leave()
			return
		}
		t.keepHome = sbx.Exists(cfg.Name)
		if t.keepHome {
			t.kept = "its removal failed"
			PrintIDEAttach(in, cfg, false)
		}
	}
}
