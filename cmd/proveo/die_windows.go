//go:build windows

// SPEC: _spec/internal/backend/exit-code.puml
package main

import "syscall"

// dieOf leaves the exit to its caller's status.
func dieOf(syscall.Signal) {}
