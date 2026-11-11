//go:build windows

// SPEC: _spec/internal/backend/teardown.puml
package sandbox

import "os/exec"

// ownGroup is a no-op: Windows delivers Ctrl+C by console, not by process group.
func ownGroup(*exec.Cmd) {}
