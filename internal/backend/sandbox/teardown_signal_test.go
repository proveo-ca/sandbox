//go:build !windows

// SPEC: _spec/internal/backend/teardown.puml
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// lockedBuffer is a terminal a test reads while teardown still writes to it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// await waits for want on the terminal.
func (l *lockedBuffer) await(t *testing.T, want string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if strings.Contains(l.String(), want) {
			return
		}
	}
	t.Fatalf("the terminal never showed %q:\n%s", want, l.String())
}

// scriptedSbx puts an sbx on PATH that lists sb, logs each call, and runs onRm for a removal.
func scriptedSbx(t *testing.T, onRm string) string {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> '" + calls + "'\n" +
		"case \"$*\" in\n" +
		"  ls*) printf 'SANDBOX AGENT STATUS\\nsb claude stopped\\n' ;;\n" +
		"  rm*) " + onRm + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, sbx.Binary), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// liveTeardown is a teardown for sandbox sb whose terminal a test may read while it runs; the
// run log it tees to is runLog.
func liveTeardown(t *testing.T, onRm string) (*teardown, *lockedBuffer, string) {
	t.Helper()
	calls := scriptedSbx(t, onRm)
	term := &lockedBuffer{}
	prev := ui.Default
	ui.Default = ui.New(term)
	restore := ui.TeeTo(&runLog)
	t.Cleanup(func() { restore(); ui.Default = prev; runLog = lockedBuffer{} })
	now := time.Now()
	return &teardown{
		in:         Input{CloneKnown: true},
		cfg:        sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: "/work"}}},
		started:    now,
		ended:      now,
		lastOutput: func() []string { return nil },
		commitHome: func() error { return nil },
	}, term, calls
}

// runLog is the run log liveTeardown tees to.
var runLog lockedBuffer

func signalSelf(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(syscall.Getpid(), sig); err != nil {
		t.Fatal(err)
	}
}

// runWatched runs td under its signal watch and returns a channel closed when it ends.
func runWatched(t *testing.T, td *teardown) <-chan struct{} {
	t.Helper()
	ctx, end := td.watch(context.Background())
	t.Cleanup(end)
	done := make(chan struct{})
	go func() {
		defer close(done)
		td.run(ctx)
		end()
	}()
	return done
}

func awaitEnd(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("teardown did not end after %s", what)
	}
}

func TestTheFirstInterruptSaysWhatLeavingKeepsAndTheSecondLeaves(t *testing.T) {
	got := captureSteps(t)
	td, term, _ := liveTeardown(t, "sleep 30")
	done := runWatched(t, td)
	term.await(t, stepRemove+" — up to")

	signalSelf(t, syscall.SIGINT)
	term.await(t, "teardown is at "+stepRemove+" — Ctrl+C again leaves now")
	term.await(t, "leaving keeps sandbox sb")
	select {
	case <-done:
		t.Fatalf("teardown left on the first interrupt:\n%s", term.String())
	case <-time.After(300 * time.Millisecond):
	}

	start := time.Now()
	signalSelf(t, syscall.SIGINT)
	awaitEnd(t, done, "the second interrupt")
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("leaving took %s; the removal in flight must end with the interrupt", took)
	}
	if sig := td.leftBy(); sig != syscall.SIGINT {
		t.Errorf("leftBy = %v, want SIGINT", sig)
	}
	if !td.keepHome {
		t.Error("keepHome = false for a sandbox the operator left in place")
	}
	for _, want := range []string{"sb — kept: teardown stopped by SIGINT at " + stepRemove, "------ ide ------", "ssh:       sb.sbx"} {
		if !strings.Contains(term.String(), want) {
			t.Errorf("the leave lines miss %q:\n%s", want, term.String())
		}
	}
	for _, logged := range []string{"sbx exec -it sb bash", "proveo rm --stop sb", "not run:"} {
		if strings.Contains(term.String(), logged) {
			t.Errorf("the operator's Ctrl+C caused this keep, so %q stays off the screen:\n%s", logged, term.String())
		}
	}
	for _, want := range []string{"sbx exec -it sb bash", "proveo rm --stop sb"} {
		if !strings.Contains(runLog.String(), want) {
			t.Errorf("the run log misses %q:\n%s", want, runLog.String())
		}
	}
	last := (*got)[len(*got)-1]
	if last.name != stepRemove || last.err == nil || errors.Is(last.err, context.DeadlineExceeded) {
		t.Errorf("last step = %q, %v; want the removal, ended by the stop", last.name, last.err)
	}
}

func TestACtrlCThatLeavesAFailedRunKeepsTheFixOnScreen(t *testing.T) {
	td, term, _ := liveTeardown(t, "exit 0")
	td.failed = true
	entered, release := make(chan struct{}), make(chan struct{})
	td.commitHome = func() error {
		close(entered)
		<-release
		return nil
	}
	done := runWatched(t, td)
	<-entered
	signalSelf(t, syscall.SIGINT)
	term.await(t, "Ctrl+C again leaves now")
	signalSelf(t, syscall.SIGINT)
	for deadline := time.Now().Add(10 * time.Second); td.asked.Load() != int32(syscall.SIGINT) && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	awaitEnd(t, done, "two interrupts on a failed run")
	if sig := td.leftBy(); sig != syscall.SIGINT {
		t.Fatalf("leftBy = %v, want SIGINT", sig)
	}
	want := "warn: sb — kept for diagnosis\n    teardown stopped by SIGINT at " + stepHome + "\n    `proveo rm sb` carries its work home and removes it\n"
	if !strings.Contains(term.String(), want) {
		t.Errorf("the agent's failure kept this sandbox, not the Ctrl+C, so its fix stays on screen:\n%s\nwant:\n%s", term.String(), want)
	}
}

func TestACtrlCThatEndsARemovalSbxFinishedClosesOnTheGoneLine(t *testing.T) {
	td, term, _ := liveTeardown(t, "exit 0")
	calls := endingSbx(t, ending{row: "sb claude stopped", rm: "touch {gone}; sleep 30"})
	done := runWatched(t, td)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(filepath.Dir(calls), "gone")); err == nil {
			break
		}
	}
	signalSelf(t, syscall.SIGINT)
	term.await(t, "Ctrl+C again leaves now")
	signalSelf(t, syscall.SIGINT)
	awaitEnd(t, done, "the second interrupt")
	got := term.String()
	if !strings.Contains(got, "warn: sb — not listed by sbx\n    teardown stopped by SIGINT at "+stepRemove+"\n") {
		t.Errorf("a sandbox sbx no longer lists closes on the gone line:\n%s", got)
	}
	for _, off := range []string{"sb — kept", "------ ide ------", "ssh:"} {
		if strings.Contains(got, off) {
			t.Errorf("a gone sandbox printed %q:\n%s", off, got)
		}
	}
	if td.keepHome || td.kept != "" || td.leftBy() != syscall.SIGINT {
		t.Errorf("keepHome %v, kept %q, leftBy %v; want nothing kept and exit 130", td.keepHome, td.kept, td.leftBy())
	}
}

func TestACtrlCWhileSbxListsNoSandboxStillKeepsTheSandbox(t *testing.T) {
	td, term, _ := liveTeardown(t, "exit 0")
	calls := endingSbx(t, ending{row: "sb claude stopped", rm: "touch {gone}; sleep 30", alone: true})
	done := runWatched(t, td)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(filepath.Dir(calls), "gone")); err == nil {
			break
		}
	}
	signalSelf(t, syscall.SIGINT)
	term.await(t, "Ctrl+C again leaves now")
	signalSelf(t, syscall.SIGINT)
	awaitEnd(t, done, "the second interrupt")
	got := term.String()
	if !strings.Contains(got, "warn: sb — kept: teardown stopped by SIGINT at "+stepRemove+"\n") || strings.Contains(got, "not listed by sbx") {
		t.Errorf("a header-only listing proves nothing, so the sandbox stays kept:\n%s", got)
	}
	if !td.keepHome || td.kept == "" {
		t.Errorf("keepHome %v, kept %q; a sandbox no listing proved gone keeps its home access and its entry", td.keepHome, td.kept)
	}
}

func TestATerminateLetsTheRunningStepFinishAndSkipsTheRest(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			got := captureSteps(t)
			td, term, calls := liveTeardown(t, "exit 0")
			entered, release := make(chan struct{}), make(chan struct{})
			td.commitHome = func() error {
				close(entered)
				<-release
				return nil
			}
			done := runWatched(t, td)
			<-entered
			signalSelf(t, sig)
			term.await(t, "stopped by "+map[syscall.Signal]string{syscall.SIGTERM: "SIGTERM", syscall.SIGHUP: "SIGHUP"}[sig]+" — leaving once "+stepHome+" finishes")
			close(release)
			awaitEnd(t, done, sig.String())

			if left := td.leftBy(); left != sig {
				t.Errorf("leftBy = %v, want %v", left, sig)
			}
			for _, s := range *got {
				if s.name == stepHome && s.err != nil {
					t.Errorf("the running step did not finish: %v", s.err)
				}
			}
			if names := topLevelSteps(*got); slices.Contains(names, stepRemove) {
				t.Errorf("the removal was recorded after a stop: %v", names)
			}
			if strings.Contains(sbxCalls(t, calls), "rm --force") {
				t.Error("the sandbox was removed after a stop")
			}
			for _, want := range []string{"sb — kept: teardown stopped by", "not run: " + stepRemove, "`proveo rm sb` carries its work home and removes it"} {
				if !strings.Contains(term.String(), want) {
					t.Errorf("the leave lines miss %q:\n%s", want, term.String())
				}
			}
		})
	}
}

func TestATerminateDuringTheLastStepLeavesNothingUndone(t *testing.T) {
	td, term, calls := liveTeardown(t, "sleep 1")
	done := runWatched(t, td)
	term.await(t, stepRemove+" — up to")
	signalSelf(t, syscall.SIGTERM)
	awaitEnd(t, done, "SIGTERM in the removal")
	if sig := td.leftBy(); sig != 0 {
		t.Errorf("leftBy = %v for a teardown that ran every step", sig)
	}
	if !strings.Contains(sbxCalls(t, calls), "rm --force sb") {
		t.Error("the removal did not run")
	}
	if strings.Contains(term.String(), "sb — kept") || !strings.Contains(term.String(), "sb — removed") {
		t.Errorf("a finished teardown said the sandbox was kept:\n%s", term.String())
	}
}

func TestAnInterruptAfterATerminateLeavesAtOnce(t *testing.T) {
	td, term, _ := liveTeardown(t, "sleep 30")
	done := runWatched(t, td)
	term.await(t, stepRemove+" — up to")
	signalSelf(t, syscall.SIGTERM)
	term.await(t, "leaving once "+stepRemove+" finishes")
	signalSelf(t, syscall.SIGINT)
	awaitEnd(t, done, "SIGTERM then SIGINT")
	if sig := td.leftBy(); sig != syscall.SIGINT {
		t.Errorf("leftBy = %v, want SIGINT", sig)
	}
}

func TestAStoppedCarryNamesTheRescueRecipe(t *testing.T) {
	td, term, _ := liveTeardown(t, "exit 0")
	repo := t.TempDir()
	td.in.Clone, td.in.RepoRoot, td.in.Sid = true, repo, "proveo-1-2"
	td.cfg.Mounts = []sbx.Mount{{Host: repo}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	td.run(ctx)
	for _, want := range []string{"sb — kept: teardown was stopped", "not run: ", "`proveo rm sb` carries its work home and removes it"} {
		if !strings.Contains(term.String(), want) {
			t.Errorf("the leave lines miss %q:\n%s", want, term.String())
		}
	}
	if strings.Contains(term.String(), "bundle create") || !strings.Contains(runLog.String(), "bundle create") {
		t.Errorf("the by-hand recipe belongs to the run log: `proveo rm` runs the same carry\nscreen:\n%s\nlog:\n%s", term.String(), runLog.String())
	}
}

func TestTeardownChildrenRunInTheirOwnProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, finish := boundCommand(ctx, time.Minute, "sh", "-c", "sleep 30 & wait")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if pgid != cmd.Process.Pid || pgid == syscall.Getpgrp() {
		t.Fatalf("child pgid = %d (pid %d, test pgrp %d); want a group of its own", pgid, cmd.Process.Pid, syscall.Getpgrp())
	}
	cancel()
	if err := finish(cmd.Wait()); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Errorf("error = %v, want a stop", err)
	}
	gone := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
			gone = true
			break
		}
	}
	if !gone {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		t.Error("the child's own child outlived the stop")
	}
}

func TestThePushedFetchStaysInTheTerminalsProcessGroup(t *testing.T) {
	cmd, finish := boundForeground(context.Background(), time.Minute, "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	_ = cmd.Process.Kill()
	_ = finish(cmd.Wait())
	if err != nil {
		t.Fatal(err)
	}
	if pgid != syscall.Getpgrp() {
		t.Errorf("pgid = %d, want the test's own group %d: git may need the terminal to ask for a credential", pgid, syscall.Getpgrp())
	}
}

// The second Ctrl+C leaves within leaveWait even when sbx is slow to list; the
// sandbox stays kept, and a failed run's fix names --stop, which works whether it runs or not.
func TestASecondCtrlCLeavesWithinItsBoundWhileSbxIsSlowToList(t *testing.T) {
	td, term, _ := liveTeardown(t, "exit 0")
	bin := t.TempDir()
	slow := filepath.Join(bin, "slow")
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  ls*) [ -e '" + slow + "' ] && sleep 30; printf 'SANDBOX AGENT STATUS\\nsb claude stopped\\nbystander claude running\\n' ;;\n" +
		"  rm*) touch '" + slow + "'; sleep 30 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, sbx.Binary), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	prev := leaveWait
	t.Cleanup(func() { leaveWait = prev })
	leaveWait = 300 * time.Millisecond
	done := runWatched(t, td)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(slow); err == nil {
			break
		}
	}
	signalSelf(t, syscall.SIGINT)
	term.await(t, "Ctrl+C again leaves now")
	start := time.Now()
	signalSelf(t, syscall.SIGINT)
	awaitEnd(t, done, "the second interrupt")
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("leaving took %s; a slow `sbx ls` must not hold the second Ctrl+C", took)
	}
	if got := term.String(); !strings.Contains(got, "warn: sb — kept: teardown stopped by SIGINT at "+stepRemove+"\n") {
		t.Errorf("an unanswered listing proves nothing, so the sandbox stays kept:\n%s", got)
	}
	if !strings.Contains(runLog.String(), "sbx did not list sb within") {
		t.Errorf("the run log does not say why the listing was not waited for:\n%s", runLog.String())
	}
}
