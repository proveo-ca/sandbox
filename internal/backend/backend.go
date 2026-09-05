// SPEC: _spec/internal/backend/exit-code.puml Package backend holds what the
// two run backends — sbx sandboxes and docker+egress — genuinely share.
//
// SPEC: _spec/internal/backend/exit-code.puml
package backend

import "fmt"

// ExitError carries the agent container's own non-zero exit code. main converts
// it into proveo's exit status so a CI step sees the AGENT's result rather than
// proveo's.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("agent exited with code %d", e.Code) }
