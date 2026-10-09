// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package ptyproxy

// CtrlCAction is what an armed Ctrl+C handler decided.
type CtrlCAction int

const (
	// CtrlCStay leaves the child running and drops the interrupt.
	CtrlCStay CtrlCAction = iota
	// CtrlCRemove ends the run so the caller can delete the sandbox.
	CtrlCRemove
	// CtrlCPass delivers the interrupt to the child.
	CtrlCPass
)
