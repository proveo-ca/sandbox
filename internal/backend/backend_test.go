package backend_test

import (
	"errors"
	"fmt"
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
