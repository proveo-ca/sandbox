// SPEC: _spec/_devops/release-gate.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"slices"
	"strings"
	"testing"
)

func TestSplitDevFlags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in         []string
		print      bool
		help       bool
		wantRemain []string
	}{
		{nil, false, false, nil},
		{[]string{"-run", "TestX"}, false, false, []string{"-run", "TestX"}},
		{[]string{"--print", "-run", "TestX"}, true, false, []string{"-run", "TestX"}},
		{[]string{"--help"}, false, true, nil},
		{[]string{"--", "--print", "-v"}, false, false, []string{"--print", "-v"}},
	}
	for _, c := range cases {
		p, h, rest := splitDevFlags(c.in)
		if p != c.print || h != c.help || !slices.Equal(rest, c.wantRemain) {
			t.Errorf("splitDevFlags(%q) = %v %v %q, want %v %v %q", c.in, p, h, rest, c.print, c.help, c.wantRemain)
		}
	}
}

func TestE2EFullRunsSuiteThenIdleThenEveryLadder(t *testing.T) {
	t.Parallel()
	plan := e2eFullPlan("150m", "45s", []string{"-run", "TestX"})
	if len(plan) != 2+len(ladderTargets) {
		t.Fatalf("plan has %d steps, want %d", len(plan), 2+len(ladderTargets))
	}
	first := plan[0].String()
	for _, needle := range []string{"PROVEO_IDLE_TEST=1", "PROVEO_SIGNAL_TEST=1", "PROVEO_TOOLCHAIN_TEST=1", "-tags=e2e ./e2e/ -count=1 -timeout 150m -run TestX"} {
		if !strings.Contains(first, needle) {
			t.Errorf("e2e step %q is missing %q", first, needle)
		}
	}
	if !strings.Contains(plan[1].String(), "TestAgentSurvivesIdleAtPrompt") {
		t.Errorf("second step is not the idle sweep: %q", plan[1])
	}
	for i, tgt := range ladderTargets {
		if !strings.Contains(plan[2+i].String(), "PROVEO_LADDER_TARGET="+tgt) {
			t.Errorf("step %d is not the %s ladder: %q", 2+i, tgt, plan[2+i])
		}
	}
	if strings.Contains(first, "PROVEO_CAPACITY_TEST") {
		t.Error("PROVEO_CAPACITY_TEST must stay off: it exhausts the host by design")
	}
}
