// SPEC: _spec/internal/backend/exit-code.puml
// Package backend holds the run errors cmd/proveo turns into exit statuses.
package backend

import (
	"fmt"
	"syscall"
)

// ExitError carries the agent container's own non-zero exit code.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("agent exited with code %d", e.Code) }

// Stopped is a run a forwarded SIGTERM or SIGHUP ended, or a teardown a signal made proveo leave.
type Stopped struct{ Signal syscall.Signal }

func (s Stopped) Error() string {
	switch s.Signal {
	case syscall.SIGTERM:
		return "stopped by SIGTERM"
	case syscall.SIGHUP:
		return "stopped by SIGHUP"
	case syscall.SIGINT:
		return "stopped by SIGINT"
	}
	return fmt.Sprintf("stopped by signal %d", int(s.Signal))
}

// Code is the shell's status for a process its signal ended: 128+signal.
func (s Stopped) Code() int { return 128 + int(s.Signal) }

// Reported is a run error proveo already printed under its heading; cmd/proveo exits 1 and prints nothing.
type Reported struct{ Err error }

func (r Reported) Error() string { return r.Err.Error() }

func (r Reported) Unwrap() error { return r.Err }
