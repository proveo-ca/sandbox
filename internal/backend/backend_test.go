package backend_test

import (
	"errors"
	"fmt"
	"syscall"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend"
)

func TestExitErrorSurvivesWrapping(t *testing.T) {
	wrapped := fmt.Errorf("sandbox teardown: %w", backend.ExitError{Code: 137})

	var ae backend.ExitError
	if !errors.As(wrapped, &ae) {
		t.Fatalf("ExitError must be recoverable through a wrap chain, got %v", wrapped)
	}
	if ae.Code != 137 {
		t.Errorf("Code = %d, want 137 — the agent's code is what CI reads", ae.Code)
	}
	if got := ae.Error(); got != "agent exited with code 137" {
		t.Errorf("Error() = %q; the operator must see the AGENT's code, not proveo's", got)
	}
}

func TestAStopExitsWith128PlusTheSignal(t *testing.T) {
	for _, c := range []struct {
		sig  syscall.Signal
		code int
		msg  string
	}{
		{syscall.SIGTERM, 143, "stopped by SIGTERM"},
		{syscall.SIGHUP, 129, "stopped by SIGHUP"},
	} {
		var st backend.Stopped
		if !errors.As(fmt.Errorf("sandbox teardown: %w", backend.Stopped{Signal: c.sig}), &st) {
			t.Fatalf("Stopped{%v} must be recoverable through a wrap chain", c.sig)
		}
		if st.Code() != c.code || st.Error() != c.msg {
			t.Errorf("Stopped{%v} = %d %q, want %d %q", c.sig, st.Code(), st.Error(), c.code, c.msg)
		}
	}
}
