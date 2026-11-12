//go:build !windows

// SPEC: _spec/internal/backend/exit-code.puml
package main

import (
	"os/signal"
	"syscall"
	"time"
)

// dieOf ends proveo by sig's default action; it returns when sig is ignored.
func dieOf(sig syscall.Signal) {
	signal.Reset(sig)
	_ = syscall.Kill(syscall.Getpid(), sig)
	time.Sleep(time.Second)
}
