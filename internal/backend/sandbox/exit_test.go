// SPEC: _spec/internal/backend/exit-code.puml
package sandbox

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestExitErrorSurvivesWrapping(t *testing.T) {
	wrapped := fmt.Errorf("sandbox teardown: %w", ExitError{Code: 137})

	var ae ExitError
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
		{syscall.SIGINT, 130, "stopped by SIGINT"},
	} {
		var st Stopped
		if !errors.As(fmt.Errorf("sandbox teardown: %w", Stopped{Signal: c.sig}), &st) {
			t.Fatalf("Stopped{%v} must be recoverable through a wrap chain", c.sig)
		}
		if st.Code() != c.code || st.Error() != c.msg {
			t.Errorf("Stopped{%v} = %d %q, want %d %q", c.sig, st.Code(), st.Error(), c.code, c.msg)
		}
	}
}
