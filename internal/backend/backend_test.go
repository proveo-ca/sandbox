package backend_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend"
)

// The whole point of the type is that it survives wrapping: a backend returns it
// from deep inside a run, and cmd/proveo has to recover the code with errors.As
// to set proveo's exit status. A value type that stopped matching through a %w
// chain would silently turn every agent failure into proveo's own exit 1.
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
