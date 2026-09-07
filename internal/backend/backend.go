// SPEC: _spec/internal/backend/exit-code.puml Package backend holds what the
// SPEC: _spec/internal/backend/exit-code.puml
package backend

import "fmt"

// ExitError carries the agent container's own non-zero exit code.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("agent exited with code %d", e.Code) }
