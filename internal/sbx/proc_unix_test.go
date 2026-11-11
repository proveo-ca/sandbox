//go:build !windows

// SPEC: _spec/internal/backend/teardown.puml
package sbx

import (
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestABoundedCallRunsInItsOwnProcessGroup(t *testing.T) {
	for name, call := range map[string]func() ([]byte, error){
		"output":   func() ([]byte, error) { return boundedWith(10*time.Second, "sh", "-c", "ps -o pgid= -p $$") },
		"combined": func() ([]byte, error) { return boundedCombinedWith(10*time.Second, "sh", "-c", "ps -o pgid= -p $$") },
	} {
		out, err := call()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		pgid, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			t.Fatalf("%s: pgid %q: %v", name, out, err)
		}
		if pgid == syscall.Getpgrp() {
			t.Errorf("%s: the child shares the test's process group %d; the terminal's Ctrl+C would reach it", name, pgid)
		}
	}
}

func TestABoundedCallThatOutlivesItsTimeoutEndsWithItsChildren(t *testing.T) {
	start := time.Now()
	_, err := boundedWith(300*time.Millisecond, "sh", "-c", "sleep 30 & wait")
	if err == nil {
		t.Fatal("a call past its timeout returned no error")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the call took %s; its timeout must end the whole group", took)
	}
}
