//go:build !windows

// SPEC: _spec/internal/schedule/schedule.puml
package sandbox

import (
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/backend"
)

func TestHangupCancelsTheRunInsteadOfKillingProveo(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGTERM} {
		ctx, stop, _, signalled := forwardStop()
		if err := syscall.Kill(syscall.Getpid(), sig); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatalf("%v did not cancel the run context", sig)
		}
		if got := signalled(); got != sig {
			t.Errorf("%v cancelled the run but reported %v", sig, got)
		}
		stop()
	}
}

func TestACancelWithoutASignalIsNotAStop(t *testing.T) {
	_, stop, cancel, signalled := forwardStop()
	defer stop()
	cancel()
	if got := signalled(); got != 0 {
		t.Errorf("a cancel from proveo itself (the Ctrl+C remove path) read as a forwarded %v", got)
	}
}

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
