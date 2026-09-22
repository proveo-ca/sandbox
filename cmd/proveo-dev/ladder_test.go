// SPEC: _spec/_devops/release-gate.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"slices"
	"strings"
	"testing"
)

func TestLadderClimbsEveryDef(t *testing.T) {
	t.Parallel()
	want := []string{"cecli", "claudecode", "codex", "cursor", "opencode"}
	if !slices.Equal(ladderTargets, want) {
		t.Fatalf("ladder targets = %v, want %v", ladderTargets, want)
	}
	plan := ladderPlan()
	if len(plan) != len(want) {
		t.Fatalf("plan has %d steps, want %d", len(plan), len(want))
	}
	for i, s := range plan {
		got := s.String()
		for _, needle := range []string{"PROVEO_LADDER_TEST=1", "PROVEO_LADDER_TARGET=" + want[i], "-run Ladder", "-tags e2e", "-timeout 30m", "-v"} {
			if !strings.Contains(got, needle) {
				t.Errorf("step %d %q is missing %q", i, got, needle)
			}
		}
	}
}

func TestLadderVerdictReadsOnlyATopLevelSkip(t *testing.T) {
	t.Parallel()
	ranButOneRungSkipped := "--- PASS: TestSandboxLadder (168.93s)\n" +
		"    --- SKIP: TestSandboxLadder/2-proveo-browser-image (0.14s)\n"
	neverRan := "    ladder_test.go:314: sbx not available: sbx not on PATH\n" +
		"--- SKIP: TestSandboxLadder (0.00s)\n"
	cases := []struct {
		name string
		log  string
		code int
		want verdict
	}{
		{"subtest skip is a pass", ranButOneRungSkipped, 0, verdict{"PASS", "cecli"}},
		{"top-level skip exits 0 and is not a pass", neverRan, 0, verdict{"SKIP", "cecli sbx not on PATH"}},
		{"skip without a reason", "--- SKIP: TestSandboxLadder (0.00s)\n", 0, verdict{"SKIP", "cecli the ladder did not run"}},
		{"red exit", "--- FAIL: TestSandboxLadder (3.00s)\n", 1, verdict{"FAIL", "cecli"}},
		{"no output, exit 0", "", 0, verdict{"PASS", "cecli"}},
		{"go missing", "", 127, verdict{"FAIL", "cecli"}},
	}
	for _, c := range cases {
		if got := ladderVerdict("cecli", c.log, c.code); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
