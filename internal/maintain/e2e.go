// SPEC: _spec/_devops/release-gate.puml, _spec/tests/testing-strategy.puml
package maintain

import (
	"cmp"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/ui"
)

// E2EAgents are the agents an e2e run narrows to; IdleAgents are the ones the idle pass covers.
var (
	E2EAgents  = []string{"claudecode", "codex", "cursor", "opencode"}
	IdleAgents = []string{"claudecode", "cursor", "opencode"}
)

// E2EOptions picks the passes of one e2e run.
type E2EOptions struct {
	Agents                        []string // empty or "all": every E2EAgents entry
	Idle, Ladder, Toolchain, Full bool
	Run                           string // go test -run for the suite pass
	Getenv                        func(string) string
}

// E2EPass is one go test process an e2e run starts.
type E2EPass struct {
	Name string
	Env  []string
	Argv []string
}

func (p E2EPass) String() string {
	return strings.TrimSpace(strings.Join(p.Env, " ") + " " + strings.Join(p.Argv, " "))
}

// E2EPlan is the suite, idle and ladder passes o selects, in that order.
func E2EPlan(o E2EOptions) ([]E2EPass, error) {
	getenv := o.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	agents, err := e2eAgents(o.Agents)
	if err != nil {
		return nil, err
	}
	var out []E2EPass
	if (!o.Idle && !o.Ladder) || o.Toolchain || o.Full {
		suite := E2EPass{Name: "suite"}
		if o.Full {
			suite.Env = append(suite.Env, "PROVEO_IDLE_TEST=1")
		}
		if o.Toolchain || o.Full {
			suite.Env = append(suite.Env, "PROVEO_TOOLCHAIN_TEST=1")
		}
		timeout := "45m"
		if o.Full {
			timeout = "150m"
		}
		timeout = cmp.Or(strings.TrimSpace(getenv("PROVEO_TEST_GO_TIMEOUT")), timeout)
		suite.Argv = []string{"go", "test", "-tags=e2e", "./e2e/", "-count=1", "-timeout", timeout}
		if o.Run != "" {
			suite.Argv = append(suite.Argv, "-run", o.Run)
		}
		out = append(out, suite)
	}
	if o.Idle || o.Full {
		if idle := intersect(agents, IdleAgents); len(idle) > 0 {
			out = append(out, E2EPass{Name: "idle",
				Env: []string{"PROVEO_IDLE_TEST=1", "PROVEO_IDLE_TARGETS=" + strings.Join(idle, ","),
					"PROVEO_IDLE_FOR=" + cmp.Or(strings.TrimSpace(getenv("PROVEO_IDLE_FOR")), "45s")},
				Argv: []string{"go", "test", "-tags=e2e", "-count=1", "-timeout", "30m", "./e2e/", "-run=^TestAgentSurvivesIdleAtPrompt$", "-v"}})
		}
	}
	if o.Ladder || o.Full {
		for _, a := range agents {
			out = append(out, E2EPass{Name: "ladder:" + a,
				Env:  []string{"PROVEO_LADDER_TEST=1", "PROVEO_LADDER_TARGET=" + a},
				Argv: []string{"go", "test", "-tags=e2e", "-count=1", "-timeout", "30m", "./e2e/", "-run=^TestSandboxLadder$", "-v"}})
		}
	}
	return out, nil
}

// e2eAgents resolves names to E2EAgents entries in their order; none or "all" is every one.
func e2eAgents(names []string) ([]string, error) {
	if len(names) == 0 || slices.Contains(names, "all") {
		return slices.Clone(E2EAgents), nil
	}
	for _, n := range names {
		if !slices.Contains(E2EAgents, n) {
			return nil, fmt.Errorf("unknown agent %q; want all or one of %s", n, strings.Join(E2EAgents, ", "))
		}
	}
	return intersect(E2EAgents, names), nil
}

// intersect keeps the entries of a that b holds, in a's order.
func intersect(a, b []string) []string {
	var out []string
	for _, s := range a {
		if slices.Contains(b, s) {
			out = append(out, s)
		}
	}
	return out
}

// RunE2E runs every pass from root, past a red one, and fails naming the red passes.
func RunE2E(passes []E2EPass, root string, printOnly bool, out io.Writer) error {
	var red []string
	for _, p := range passes {
		if printOnly {
			fmt.Fprintf(out, "# %s\n%s\n", p.Name, p)
			continue
		}
		ui.Appf("e2e %s", p.Name)
		cmd := exec.Command(p.Argv[0], p.Argv[1:]...)
		cmd.Dir, cmd.Stdout, cmd.Stderr = root, os.Stdout, os.Stderr
		cmd.Env = append(os.Environ(), p.Env...)
		if err := cmd.Run(); err != nil {
			ui.Failf("e2e %s: %v", p.Name, err)
			red = append(red, p.Name)
		}
	}
	if len(red) > 0 {
		return fmt.Errorf("red e2e passes: %s", strings.Join(red, ", "))
	}
	return nil
}

// ImageGatePlan snapshots the CLI, stages the CDN tree, then runs the installer suite and every image suite.
func ImageGatePlan() []Command {
	return []Command{
		{Argv: []string{"goreleaser", "release", "--snapshot", "--clean"}},
		{Argv: []string{"go", "run", "./internal/cdn/stage"}},
		{Argv: []string{"go", "test", "-count=1", "./internal/installtest/"}},
		{Argv: []string{"go", "test", "-tags=image", "-count=1", "-timeout", "60m", "./internal/imagetest/"}},
	}
}

// RunImageGate runs ImageGatePlan and stops at the first red step.
func RunImageGate(printOnly bool, out io.Writer) error {
	if printOnly {
		fmt.Fprintln(out, "# image gate")
	} else {
		ui.Appf("image gate: snapshot, stage, installer suite, every image suite")
	}
	return RunPlan(ImageGatePlan(), PlanIO{PrintOnly: printOnly, Out: out, Env: ImageGateEnv})
}

// ImageGateEnv points the installer suite at the tree ImageGatePlan stages.
var ImageGateEnv = []string{"PROVEO_INSTALLTEST_CDN=apps/cli/public/cli"}
