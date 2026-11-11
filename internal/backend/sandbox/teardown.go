// SPEC: _spec/internal/backend/teardown.puml
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
	if ctx.Err() != nil {
		recordStep(name, 0, errStepSkipped)
		return errStepSkipped
	}
	if name != stepClone {
		announce(name)
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
	_ = t.do(ctx, stepClone, func() error { return TeardownClone(in, cfg) })
	_ = t.do(ctx, stepState, func() error {
		_, err := t.saveState(ctx)
		return err
	})
	if err := t.do(ctx, stepHome, t.commitHome); err != nil {
		ui.Warnf("home-root config not preserved: %v", err)
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
		t.keepHome = true
		PrintIDEAttach(in, cfg, false)
	}
}

// normalEnd carries the work home, then removes the sandbox unless work or history stays in it.
func (t *teardown) normalEnd(ctx context.Context) {
	in, cfg := t.in, t.cfg
	stranded := t.do(ctx, stepClone, func() error { return TeardownClone(in, cfg) })
	var saved string
	if err := t.do(ctx, stepState, func() (err error) {
		saved, err = t.saveState(ctx)
		return err
	}); err != nil {
		ui.Warnf("resume state not preserved (%v): %s", err, strings.TrimSpace(saved))
	}
	if err := t.do(ctx, stepHome, t.commitHome); err != nil {
		ui.Warnf("home-root config not preserved: %v", err)
	}
	switch normalEndOf(t.forced, stranded, opencodeTarget(in.Target)) {
	case endKeepStranded:
		t.keepHome = true
		if opencodeTarget(in.Target) {
			ui.Warnf("%v — %s kept with its OpenCode history", stranded, cfg.Name)
		} else {
			ui.Warnf("%v — %s kept; `sbx rm --force %s` deletes it once the work is home", stranded, cfg.Name, cfg.Name)
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
		t.keepHome = sbx.Exists(cfg.Name)
		if t.keepHome {
			PrintIDEAttach(in, cfg, false)
		}
	}
}
