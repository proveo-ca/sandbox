// SPEC: _spec/internal/backend/exit-code.puml
//
// Package backend holds what the two run backends — sbx sandboxes and
// docker+egress — genuinely share. That is one type, and after all six
// decomposition moves it is still one type, on purpose.
//
// NO Plan/Execute INTERFACE IS DECLARED HERE, and the earlier promise of one is
// withdrawn. Nothing consumes the backends polymorphically: run.selectBackend
// branches on which backend won and calls each by name, because their shapes
// genuinely differ — sandbox.Run takes an Input and runs, while dockeregress
// splits into Assemble then Exec so the caller can start a review gate between
// the two. An interface with two divergent implementers and no caller that holds
// one is a guess about shape, not a description of it.
//
// The package exists for a dependency reason rather than an abstraction one:
// cmd/proveo and both backends need ExitError, so it cannot live in either
// backend without making them depend on each other.
package backend

import "fmt"

// ExitError carries the agent container's own non-zero exit code.
//
// main converts it into proveo's exit status so a CI step sees the AGENT's
// result rather than proveo's. Without a shared type each backend would define
// its own and cmd/proveo would convert between them — code that exists only
// because a type sits in the wrong package.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("agent exited with code %d", e.Code) }
