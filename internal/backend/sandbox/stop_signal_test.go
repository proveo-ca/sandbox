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
		ctx, stop := forwardStop()
		if err := syscall.Kill(syscall.Getpid(), sig); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatalf("%v did not cancel the run context", sig)
		}
		stop()
	}
}
