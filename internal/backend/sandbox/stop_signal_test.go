//go:build !windows

// SPEC: _spec/internal/schedule/schedule.puml
package sandbox

import (
	"errors"
	"syscall"
	"testing"
	"time"
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
		if !signalled() {
			t.Errorf("%v cancelled the run but did not report the signal", sig)
		}
		stop()
	}
}

func TestACancelWithoutASignalIsNotAStop(t *testing.T) {
	_, stop, cancel, signalled := forwardStop()
	defer stop()
	cancel()
	if signalled() {
		t.Error("a cancel from proveo itself (the Ctrl+C remove path) must not read as a forwarded stop")
	}
}

func TestAForwardedStopEndsLikeANormalExit(t *testing.T) {
	failed := errors.New("context canceled")
	for _, c := range []struct {
		name              string
		signalled, forced bool
		want              error
	}{
		{"stopped by SIGTERM or SIGHUP", true, false, nil},
		{"the Ctrl+C remove path", true, true, failed},
		{"an agent that failed on its own", false, false, failed},
	} {
		if got := stopOutcome(failed, c.signalled, c.forced); got != c.want {
			t.Errorf("%s: stopOutcome = %v, want %v", c.name, got, c.want)
		}
	}
}
