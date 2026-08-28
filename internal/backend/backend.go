// SPEC: _spec/_plans/main-decomposition-moves.puml
//
// Package backend is what the two run backends — sbx sandboxes and
// docker+egress — have in common. Today that is one type; moves 4 and 5 fill in
// the rest.
//
// The interface the plan describes is Plan(RunSpec) then Execute(Plan), and it is
// deliberately NOT declared yet: RunSpec arrives with move 6, and an interface
// written before its second implementer exists is a guess about the shape rather
// than a description of it. ExitError is here now because it is already shared —
// both backends return it and cmd/proveo reads it to set proveo's own exit code.
package backend

import "fmt"

// ExitError carries the agent container's own non-zero exit code.
//
// It has to live above both backends: each returns it, and main converts it into
// proveo's exit status so a CI step sees the agent's result rather than proveo's.
// Leaving it in cmd/proveo would have forced every backend to define its own and
// cmd/proveo to convert — code that exists only because a type sits in the wrong
// package.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("agent exited with code %d", e.Code) }
