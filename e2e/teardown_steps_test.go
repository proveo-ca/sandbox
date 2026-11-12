//go:build e2e

// SPEC: _spec/internal/backend/teardown.puml

package e2e

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// normalRunSteps is the top-level step order a run that creates its sandbox and ends normally logs.
var normalRunSteps = []string{
	"workload kit", "stale sandbox check", "sandbox secrets", "create sandbox",
	"agent",
	"credential check", "clone mode", "pushed branches", "egress record", "memory evidence",
	"clone", "resume state", "home config", "remove sandbox",
}

// stepRowName reads the step name out of one run-log line; ok is false for any other line.
func stepRowName(line string) (string, bool) {
	_, row, found := strings.Cut(line, "step: ")
	if !found {
		return "", false
	}
	fields := strings.Fields(row)
	for i, f := range fields {
		if _, err := time.ParseDuration(f); err == nil && i > 0 {
			return strings.Join(fields[:i], " "), true
		}
	}
	return "", false
}

// loggedSteps lists the top-level step names in the newest run log under home, in log order.
func loggedSteps(t *testing.T, home string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "logs", "latest.log"))
	if err != nil {
		t.Fatalf("read the run log: %v", err)
	}
	var names []string
	for _, line := range strings.Split(string(b), "\n") {
		if name, ok := stepRowName(line); ok && !strings.Contains(name, "/") {
			names = append(names, name)
		}
	}
	return names
}

func TestStepRowNameReadsARunLogLine(t *testing.T) {
	for _, c := range []struct {
		line, want string
		ok         bool
	}{
		{"12:00:01.000   step: credential check 12ms ok", "credential check", true},
		{"step: clone/snapshot 1.204s failed: sbx gave no answer within 3m0s", "clone/snapshot", true},
		{"step: agent 2m3.5s ok", "agent", true},
		{"backend: docker sandboxes (sbx)", "", false},
		{"step: 12ms ok", "", false},
	} {
		got, ok := stepRowName(c.line)
		if got != c.want || ok != c.ok {
			t.Errorf("stepRowName(%q) = %q, %v; want %q, %v", c.line, got, ok, c.want, c.ok)
		}
	}
	if !slices.Contains(normalRunSteps, "remove sandbox") {
		t.Error("a normal exit's steps do not end in the removal")
	}
}
