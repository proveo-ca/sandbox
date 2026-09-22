// SPEC: _spec/_devops/release-gate.puml, _spec/internal/sbx/sandbox-backend.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"slices"
	"strings"
	"testing"
)

func TestIdleSweepsTheFourDefsPast30s(t *testing.T) {
	t.Parallel()
	got := idleStep("45s").String()
	for _, needle := range []string{
		"PROVEO_IDLE_TEST=1",
		"PROVEO_IDLE_TARGETS=cecli,claudecode,cursor,opencode",
		"PROVEO_IDLE_FOR=45s",
		"-run TestAgentSurvivesIdleAtPrompt",
		"-timeout 30m",
	} {
		if !strings.Contains(got, needle) {
			t.Errorf("idle step %q is missing %q", got, needle)
		}
	}
}

func TestIdleVerdicts(t *testing.T) {
	t.Parallel()
	allPass := "    --- PASS: TestAgentSurvivesIdleAtPrompt/cecli (50s)\n" +
		"    --- PASS: TestAgentSurvivesIdleAtPrompt/claudecode (50s)\n" +
		"    --- PASS: TestAgentSurvivesIdleAtPrompt/cursor (50s)\n" +
		"    --- PASS: TestAgentSurvivesIdleAtPrompt/opencode (50s)\n" +
		"--- PASS: TestAgentSurvivesIdleAtPrompt (200s)\n"
	mixed := "    --- PASS: TestAgentSurvivesIdleAtPrompt/cecli (50s)\n" +
		"    --- FAIL: TestAgentSurvivesIdleAtPrompt/claudecode (50s)\n" +
		"    --- SKIP: TestAgentSurvivesIdleAtPrompt/cursor (0s)\n"
	cases := []struct {
		name       string
		log        string
		code       int
		want       []string
		wantFailed bool
	}{
		{"all pass", allPass, 0, []string{"PASS cecli", "PASS claudecode", "PASS cursor", "PASS opencode"}, false},
		{"all pass but suite red", allPass, 2, []string{"PASS cecli", "PASS claudecode", "PASS cursor", "PASS opencode", "FAIL suite exit 2"}, true},
		{"mixed", mixed, 1, []string{"PASS cecli", "FAIL claudecode", "SKIP cursor", "MISS opencode — no subtest result"}, true},
		{"top-level skip", "--- SKIP: TestAgentSurvivesIdleAtPrompt (0.00s)\n", 0, []string{"SKIP all — the idle suite did not run"}, true},
		{"indented top-level name is not a skip-all", "    --- SKIP: TestAgentSurvivesIdleAtPrompt/cecli (0s)\n", 0,
			[]string{"SKIP cecli", "MISS claudecode — no subtest result", "MISS cursor — no subtest result", "MISS opencode — no subtest result"}, true},
	}
	for _, c := range cases {
		vs, failed := idleVerdicts(c.log, c.code)
		got := make([]string, len(vs))
		for i, v := range vs {
			got[i] = v.String()
		}
		if !slices.Equal(got, c.want) || failed != c.wantFailed {
			t.Errorf("%s: got %q failed=%v, want %q failed=%v", c.name, got, failed, c.want, c.wantFailed)
		}
	}
}
