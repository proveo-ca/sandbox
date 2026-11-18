// SPEC: _spec/tests/testing-strategy.puml, _spec/_devops/release-gate.puml
package maintain

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func env(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

func names(ps []E2EPass) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

func plan(t *testing.T, o E2EOptions) []E2EPass {
	t.Helper()
	if o.Getenv == nil {
		o.Getenv = env(nil)
	}
	ps, err := E2EPlan(o)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func TestE2EPlanRunsTheSuiteAloneByDefault(t *testing.T) {
	t.Parallel()
	ps := plan(t, E2EOptions{})
	if len(ps) != 1 || ps[0].String() != "go test -tags=e2e ./e2e/ -count=1 -timeout 45m" {
		t.Errorf("a bare e2e plans %q, want the ungated suite at 45m", ps)
	}
	ps = plan(t, E2EOptions{Run: "TestHello", Ladder: true, Toolchain: true})
	if got := ps[0].String(); got != "PROVEO_TOOLCHAIN_TEST=1 go test -tags=e2e ./e2e/ -count=1 -timeout 45m -run TestHello" {
		t.Errorf("suite with -toolchain -run = %q", got)
	}
	for _, p := range ps[1:] {
		if slices.Contains(p.Argv, "TestHello") {
			t.Errorf("-run reached %s: %q", p.Name, p.Argv)
		}
	}
	if only := plan(t, E2EOptions{Ladder: true}); slices.Contains(names(only), "suite") {
		t.Errorf("--ladder alone plans %q, want the ladder without the suite", names(only))
	}
}

func TestE2EPlanFullIsTheSilentDeathGate(t *testing.T) {
	t.Parallel()
	ps := plan(t, E2EOptions{Full: true})
	want := []string{"suite", "idle", "ladder:claudecode", "ladder:codex", "ladder:cursor", "ladder:opencode"}
	if got := names(ps); !slices.Equal(got, want) {
		t.Fatalf("--full plans %q, want %q: one process per pass, every ladder def in its own", got, want)
	}
	if got := ps[0].String(); got != "PROVEO_IDLE_TEST=1 PROVEO_TOOLCHAIN_TEST=1 go test -tags=e2e ./e2e/ -count=1 -timeout 150m" {
		t.Errorf("--full suite = %q, want both gates on at 150m", got)
	}
	idle := ps[1]
	if !slices.Contains(idle.Env, "PROVEO_IDLE_TEST=1") || !slices.Contains(idle.Env, "PROVEO_IDLE_TARGETS=claudecode,cursor,opencode") ||
		!slices.Contains(idle.Argv, "-run=^TestAgentSurvivesIdleAtPrompt$") {
		t.Errorf("--full idle = %q, want PROVEO_IDLE_TEST=1 over claudecode,cursor,opencode", idle)
	}
	for _, kv := range idle.Env {
		if v, ok := strings.CutPrefix(kv, "PROVEO_IDLE_FOR="); ok {
			if d, err := time.ParseDuration(v); err != nil || d <= 30*time.Second {
				t.Errorf("idle waits %q by default, want over 30s: the silent death lands past 30s", v)
			}
		}
	}
	for _, p := range ps[2:] {
		def := strings.TrimPrefix(p.Name, "ladder:")
		if got := strings.Join(p.Env, " "); got != "PROVEO_LADDER_TEST=1 PROVEO_LADDER_TARGET="+def {
			t.Errorf("%s env = %q, want PROVEO_LADDER_TEST=1 PROVEO_LADDER_TARGET=%s", p.Name, got, def)
		}
		if got := strings.Join(p.Argv, " "); got != "go test -tags=e2e -count=1 -timeout 30m ./e2e/ -run=^TestSandboxLadder$ -v" {
			t.Errorf("%s runs %q, want its own 30m ladder process", p.Name, got)
		}
	}
}

func TestE2EPlanNarrowsIdleAndLadderToTheAgents(t *testing.T) {
	t.Parallel()
	ps := plan(t, E2EOptions{Idle: true, Ladder: true, Agents: []string{"cursor", "codex"}})
	if got, want := names(ps), []string{"idle", "ladder:codex", "ladder:cursor"}; !slices.Equal(got, want) {
		t.Errorf("cursor codex plans %q, want %q in registry order", got, want)
	}
	if !slices.Contains(ps[0].Env, "PROVEO_IDLE_TARGETS=cursor") {
		t.Errorf("idle over cursor codex = %q, want cursor alone: codex has no idle pass", ps[0].Env)
	}
	if got := names(plan(t, E2EOptions{Idle: true, Agents: []string{"codex"}})); len(got) != 0 {
		t.Errorf("--idle codex plans %q, want nothing: the idle pass covers claudecode, cursor and opencode", got)
	}
	if got := names(plan(t, E2EOptions{Ladder: true, Agents: []string{"all", "codex"}})); len(got) != 4 {
		t.Errorf("all codex plans %q, want every ladder def", got)
	}
	if _, err := E2EPlan(E2EOptions{Ladder: true, Agents: []string{"hermes"}}); err == nil || !strings.Contains(err.Error(), `unknown agent "hermes"`) {
		t.Errorf("E2EPlan(hermes) = %v, want an unknown-agent refusal", err)
	}
}

func TestE2EPlanTreatsAnEmptyOperatorValueAsUnset(t *testing.T) {
	t.Parallel()
	blank := plan(t, E2EOptions{Full: true, Getenv: env(map[string]string{"PROVEO_TEST_GO_TIMEOUT": "  ", "PROVEO_IDLE_FOR": " "})})
	if !slices.Contains(blank[0].Argv, "150m") || !slices.Contains(blank[1].Env, "PROVEO_IDLE_FOR=45s") {
		t.Errorf("blank operator values planned %q and %q, want the 150m and 45s fallbacks", blank[0], blank[1])
	}
	set := plan(t, E2EOptions{Full: true, Getenv: env(map[string]string{"PROVEO_TEST_GO_TIMEOUT": "200m", "PROVEO_IDLE_FOR": "2m"})})
	if !slices.Contains(set[0].Argv, "200m") || !slices.Contains(set[1].Env, "PROVEO_IDLE_FOR=2m") {
		t.Errorf("set operator values planned %q and %q, want 200m and 2m", set[0], set[1])
	}
}

func TestImageGatePlanRunsEveryImageSuite(t *testing.T) {
	t.Parallel()
	want := []string{
		"goreleaser release --snapshot --clean",
		"go run ./internal/cdn/stage",
		"go test -count=1 ./internal/installtest/",
		"go test -tags=image -count=1 -timeout 60m ./internal/imagetest/",
	}
	if got := argvs(ImageGatePlan()); !slices.Equal(got, want) {
		t.Errorf("ImageGatePlan = %q, want %q: a fresh snapshot stage, then every TestImage* with no -run", got, want)
	}
	if !slices.Equal(ImageGateEnv, []string{"PROVEO_INSTALLTEST_CDN=apps/cli/public/cli"}) {
		t.Errorf("ImageGateEnv = %q; the installer suite would stage its own CDN", ImageGateEnv)
	}
	var out bytes.Buffer
	if err := RunImageGate(true, &out); err != nil || out.String() != "# image gate\n"+strings.Join(want, "\n")+"\n" {
		t.Errorf("RunImageGate(print) = %v, printed\n%s", err, out.String())
	}
}

func TestRunE2EContinuesPastARedPass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ran := filepath.Join(dir, "ran")
	err := RunE2E([]E2EPass{
		{Name: "red", Argv: []string{"false"}},
		{Name: "after", Argv: []string{"touch", ran}},
	}, dir, false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "red e2e passes: red") || strings.Contains(err.Error(), "after") {
		t.Errorf("RunE2E = %v, want a failure naming the red pass alone", err)
	}
	if _, serr := os.Stat(ran); serr != nil {
		t.Error("a red pass stopped the next one")
	}
}
