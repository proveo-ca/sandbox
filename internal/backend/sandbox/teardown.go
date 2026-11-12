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
	status int  // `sbx run`'s exit status; 0 when it was not an exit

	started, ended time.Time
	lastOutput     func() []string
	altScreen      func() bool // the agent drew on the alternate screen; nil for never
	commitHome     func() error
	results        *Block // what came home, printed once the carry ends

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
	if t.results == nil {
		t.results = NewBlock(ResultsHeading)
	}
	recordKept(t.in, cfg.Name, "its teardown did not finish", true)
	defer func() { recordKept(t.in, cfg.Name, t.kept, false) }()
	if lines := t.lastOutput(); len(lines) > 0 {
		ui.Logf("last output from the agent:")
		for _, l := range lines {
			ui.Logf("  %s", l)
		}
	}
	ui.Section(ui.SectionResults)
	_ = t.do(ctx, stepCredential, func() error { reportCredentialInUse(t.in.Target, cfg.Name, t.results); return nil })
	_ = t.do(ctx, stepCloneMode, func() error {
		clones, err := sandboxClone(t.in, cfg.Name)
		if err == nil {
			t.in.Clone, t.in.CloneKnown = clones, true
		}
		return err
	})
	_ = t.do(ctx, stepPushed, func() error { reportPushedBranches(t.in, cfg, t.started, t.results); return nil })
	_ = t.do(ctx, stepEgress, func() error { CapturePolicyLog(t.in.RunDir, cfg.Name); return nil })
	_ = t.do(ctx, stepMemory, func() error { CaptureMemoryEvidence(t.in.RunDir, cfg.Name, t.results); return nil })
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
			ui.Warnf("%v — leaving once %s finishes", backend.Stopped{Signal: sig}, at)
		}
	case t.interrupts.Add(1) == 1 && t.asked.Load() == 0:
		ui.Warnf("teardown is at %s — Ctrl+C again leaves now", at)
		ui.Detailf("leaving keeps sandbox %s; work it has not carried home stays only there", t.cfg.Name)
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

// leave closes a stopped teardown on the sandbox it keeps: why, the agent's last lines, the steps
// not run, the fix unless the operator's Ctrl+C alone caused the keep, then its ide block. A
// sandbox sbx's listing proves gone closes on the gone line.
func (t *teardown) leave() {
	t.results.Print()
	name := t.cfg.Name
	sig := syscall.Signal(t.asked.Load())
	reason := "teardown was stopped"
	if sig != 0 {
		reason = "teardown " + backend.Stopped{Signal: sig}.Error()
	}
	if at := t.askedAt.Load(); at != nil {
		reason += " at " + *at
	}
	t.left = true
	if sbx.Gone(name) {
		gone(name, reason).print()
		return
	}
	it := Item{Warn: true, Text: name + " — kept: " + reason, Log: []string{lookInside(name)}}
	t.kept = reason
	if t.failed {
		it.Text, it.Detail, t.kept = name+" — kept for diagnosis", append([]string{reason}, t.tail()...), "the run failed; "+reason
	}
	if rest := t.notRun(); len(rest) > 0 {
		it.Detail = append(it.Detail, "not run: "+strings.Join(rest, ", "))
	}
	if sig == syscall.SIGINT && !t.failed {
		it.Log = append(it.Log, keptFix(name, true)+"; `proveo rm "+name+"` once it is stopped")
	} else {
		it.Detail = append(it.Detail, keptFix(name, sbx.Running(name)))
	}
	if errors.Is(t.carry, errStepSkipped) && (t.in.Clone || !t.in.CloneKnown) {
		it.Log = append(it.Log, CloneRescueLines(name, t.in.Sid, agentWorkdir(t.in, t.cfg.Mounts), t.in.RepoRoot)...)
	}
	t.keepHome = true
	t.closeKept(it)
}

// lookInside is the run-log line that opens a shell in a kept sandbox.
func lookInside(name string) string { return "`sbx exec -it " + name + " bash` looks inside" }

// closeKept ends the results block on the kept sandbox's line, then prints the ide block that attaches to it.
func (t *teardown) closeKept(it Item) {
	it.print()
	PrintBlock(IDEHeading, ideFacts(t.in, t.cfg), nil)
}

// tail is the agent's last five lines, past sbx's own error line, when the agent drew on the
// alternate screen; nil otherwise.
func (t *teardown) tail() []string {
	if t.altScreen == nil || !t.altScreen() {
		return nil
	}
	lines := slices.Clone(t.lastOutput())
	if sbx.SaidError(t.status, lines) {
		lines = lines[:len(lines)-1]
	}
	return lines[max(0, len(lines)-5):]
}

// gone is the closing line for a sandbox sbx's listing proves gone.
func gone(name string, detail ...string) Item {
	return Item{Warn: true, Text: name + " — not listed by sbx", Detail: detail}
}

// saveState is SaveState under ctx.
func (t *teardown) saveState(ctx context.Context) (string, error) {
	return SaveState(t.cfg.Name, t.cfg.Env, sbx.Exists(t.cfg.Name), func(args ...string) (string, error) {
		return guestRunIn(ctx, args...)
	})
}

// failedEnd carries what it can home, saves the agent's state (its transcript with it), prints
// what came home, and keeps the sandbox for diagnosis.
func (t *teardown) failedEnd(ctx context.Context) {
	in, cfg := t.in, t.cfg
	said := len(t.lastOutput()) > 0
	restarted := !sbx.Running(cfg.Name)
	t.carry = t.do(ctx, stepClone, func() error { return TeardownClone(in, cfg, t.results) })
	_ = t.do(ctx, stepState, func() error {
		_, err := t.saveState(ctx)
		return err
	})
	if tr := credentials.AgentTranscript(in.Target, in.HomeRoot, t.started, t.ended); tr != "" {
		said = true
		t.results.Fact(ui.Fact{Role: ui.RoleStore, Key: "transcript", Value: ui.Tilde(tr)})
	} else if restarted && sbx.Exists(cfg.Name) {
		ui.Logf("no transcript from this run — the sandbox had already stopped, " +
			"so state was copied out after it ended and anything newer than the run is the harvest's own")
	}
	if !said {
		if hint := credentials.NoCredentialHint(in.Man, in.Target, in.HomeRoot, cfg.Env, t.secrets,
			sbx.StoredServices(), in.Lookup); len(hint) > 0 {
			t.results.Item(noCredentialItem(hint))
		}
	}
	t.results.Print()
	t.homeStep(ctx)
	if t.mustLeave(ctx) {
		t.leave()
		return
	}
	if sbx.Gone(cfg.Name) {
		gone(cfg.Name).print()
		return
	}
	t.keepHome, t.kept = true, "the run failed"
	t.closeKept(Item{Warn: true, Text: cfg.Name + " — kept for diagnosis",
		Detail: append(t.tail(), keptFix(cfg.Name, sbx.Running(cfg.Name))), Log: []string{lookInside(cfg.Name)}})
}

// noCredentialItem is NoCredentialHint as one ⚠ item: its verdict, then its reason and each
// variable's remedy dim; the closing note on empty store entries goes to the run log.
func noCredentialItem(hint []string) Item {
	head, why, _ := strings.Cut(hint[0], " — ")
	it := Item{Warn: true, Text: head}
	if why != "" {
		it.Detail = append(it.Detail, why)
	}
	body := hint[1:]
	if n := len(body); n >= 2 {
		body, it.Log = body[:n-2], []string{strings.TrimSpace(body[n-2]) + " " + strings.TrimSpace(body[n-1])}
	}
	for _, l := range body {
		it.Detail = append(it.Detail, strings.TrimSpace(l))
	}
	return it
}

// homeStep commits the home-root config, and says so when it could not.
func (t *teardown) homeStep(ctx context.Context) {
	if err := t.do(ctx, stepHome, t.commitHome); err != nil && !errors.Is(err, errStepSkipped) {
		Item{Warn: true, Text: "home-root config — not preserved", Detail: []string{ui.TildeIn(err.Error())}}.print()
	}
}

// normalEnd carries the work home, prints what came home, then removes the sandbox unless
// work or history stays in it; its last line is the sandbox's fate.
func (t *teardown) normalEnd(ctx context.Context) {
	in, cfg := t.in, t.cfg
	stranded := t.do(ctx, stepClone, func() error { return TeardownClone(in, cfg, t.results) })
	t.carry = stranded
	t.results.Print()
	var saved string
	if err := t.do(ctx, stepState, func() (err error) {
		saved, err = t.saveState(ctx)
		return err
	}); err != nil && !errors.Is(err, errStepSkipped) {
		Item{Warn: true, Text: "resume state — not saved", Detail: []string{failure(err, saved)}}.print()
	}
	t.homeStep(ctx)
	if t.mustLeave(ctx) {
		t.leave()
		return
	}
	switch normalEndOf(t.forced, stranded, opencodeTarget(in.Target)) {
	case endKeepStranded:
		t.keepHome, t.kept = true, stranded.Error()
		why := stranded.Error()
		if opencodeTarget(in.Target) {
			why += ", with its OpenCode history"
		}
		t.closeKept(Item{Warn: true, Text: cfg.Name + " — kept: " + why,
			Detail: []string{keptFix(cfg.Name, sbx.Running(cfg.Name))}, Log: []string{lookInside(cfg.Name)}})
		return
	case endRetainEngine:
		t.keepHome = true
		Item{Ok: true, Text: "retained engine " + cfg.Name + " — holds OpenCode's history",
			Log: []string{"`sbx rm --force " + cfg.Name + "` deletes it and its history"}}.print()
		return
	}
	if t.forced {
		ui.Logf("sbx rm --force %s — deleting the session", cfg.Name)
	}
	err := t.do(ctx, stepRemove, func() error { return removeSandbox(ctx, cfg.Name) })
	switch {
	case err == nil && stranded != nil:
		Item{Warn: true, Text: cfg.Name + " — removed", Detail: []string{"what did not come home went with it"}}.print()
		return
	case err == nil && t.forced && opencodeTarget(in.Target):
		Item{Ok: true, Text: cfg.Name + " — removed with its OpenCode history"}.print()
		return
	case err == nil:
		Item{Ok: true, Text: cfg.Name + " — removed"}.print()
		return
	case t.mustLeave(ctx):
		t.leave()
		return
	}
	if sbx.Gone(cfg.Name) {
		gone(cfg.Name, "its removal failed: "+ui.TildeIn(err.Error()), "`sbx ls` shows whether it is gone").print()
		return
	}
	t.keepHome, t.kept = true, "its removal failed"
	fix := keptFix(cfg.Name, sbx.Running(cfg.Name))
	if errors.Is(err, context.DeadlineExceeded) {
		fix = rescueRemoval(cfg.Name)
	}
	t.closeKept(Item{Warn: true, Text: cfg.Name + " — kept: its removal failed",
		Detail: []string{ui.TildeIn(err.Error()), fix}, Log: []string{lookInside(cfg.Name)}})
}
