//go:build !windows

// SPEC: _spec/internal/schedule/schedule.puml
package sandbox

import (
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
