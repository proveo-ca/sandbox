//go:build e2e

// SPEC: _spec/tests/42-hello-world-e2e.puml, _spec/tests/testing-strategy.puml

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestHelloWorldE2E drives each harness against a COPY of tests/e2e/samples with
// a real Anthropic key and asserts two observable facts per target — never the
// model's prose:
//
//	bind mount works → the hello-world file the agent was asked for shows up on
//	                   the HOST, with the run's marker inside it
//	models bridged   → the entrypoint's PROVEO_MODELS line names the tiers the
//	                   project .env suggested (ARCHITECT_MODEL / EDITOR_MODEL /
//	                   SMALL_MODEL), not the harness's baked-in default
//
// Targets run in dependency-of-confidence order: cecli (simplest loop), then
// opencode, then claudecode.
//
//	go test -tags=e2e ./tests/e2e/ -run HelloWorldE2E -v -timeout 40m
//
// Credentials never reach an argv: the run gets a filtered, 0600 .env holding
// only ANTHROPIC_API_KEY plus the non-secret model aliases, handed over as
// PROVEO_EGRESS_ENV_FILE. Every other provider key is explicitly UNSET in the
// child, because firewall-mode brokering requires exactly one detected provider
// (see brokerProvider in cmd/proveo) — a multi-provider .env would silently
// disable the broker and hand the agent a sentinel with nothing behind it.
func TestHelloWorldE2E(t *testing.T) {
	requireTmux(t)
	requireDocker(t)
	// This suite spends real tokens; no key in the environment or the repo .env
	// means there is nothing to spend, so it is a skip rather than a failure.
	if hostEnvValue(t, "ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not in the environment or the repo .env")
	}

	proveoBin := buildProveo(t)
	mode := env("PROVEO_TEST_EGRESS_MODE", "firewall")

	for _, h := range helloHarnesses {
		t.Run(h.target, func(t *testing.T) {
			harnessImage(t, h.target)
			runHelloWorld(t, h, proveoBin, mode)
		})
	}
}

// helloHarness is one target's non-interactive contract: how to hand it a task
// from argv, where it is told to write, and which host paths prove the mount.
type helloHarness struct {
	target string
	// agentArgs are forwarded after `--` (the harness's own one-shot form).
	agentArgs func(prompt string) []string
	// promptPath is the path the PROMPT names — container-absolute where the
	// layout makes "the current directory" ambiguous.
	promptPath string
	// hostPaths are checked (in order) under the mounted workspace on the host.
	hostPaths []string
	// smallFrom is the .env tier this harness bridges into its small/weak slot.
	// opencode prefers EDITOR_MODEL there and falls back to SMALL_MODEL
	// (ApplyEnvBridges); cecli and claudecode map SMALL_MODEL directly.
	smallFrom string
}

var helloHarnesses = []helloHarness{
	{
		target:     "cecli",
		agentArgs:  func(p string) []string { return []string{"--yes-always", "--message", p} },
		promptPath: "HELLO_WORLD.txt",
		hostPaths:  []string{"HELLO_WORLD.txt"},
		smallFrom:  "SMALL_MODEL",
	},
	{
		target:     "opencode",
		agentArgs:  func(p string) []string { return []string{"run", "--auto", "--agent", "build", p} },
		promptPath: "HELLO_WORLD.txt",
		hostPaths:  []string{"HELLO_WORLD.txt"},
		smallFrom:  "EDITOR_MODEL",
	},
	{
		// input-output layout: /workspace itself is not a mount, so the prompt
		// names the writable one explicitly.
		target:     "claudecode",
		agentArgs:  func(p string) []string { return []string{"-p", p} },
		promptPath: "/workspace/output/HELLO_WORLD.txt",
		hostPaths:  []string{"reports/HELLO_WORLD.txt", "HELLO_WORLD.txt"},
		smallFrom:  "SMALL_MODEL",
	},
}

func runHelloWorld(t *testing.T, h helloHarness, proveoBin, mode string) {
	t.Helper()

	work := copySampleWorkspace(t)
	// tests/e2e/samples/.env is a symlink to the repo root .env, so `cp -a` lands a
	// DANGLING link in the copy — a broken bind-mount destination in broker mode and
	// a broken mask target in firewall. Replace it with this run's filtered env so
	// the workspace is self-consistent and still single-provider.
	replaceSampleEnv(t, work)
	mustRun(t, work, "git", "init", "-q", ".")
	mustRun(t, work, "git", "config", "user.email", "e2e@proveo.test")
	mustRun(t, work, "git", "config", "user.name", "proveo e2e")
	// claudecode's output mount (and cecli's /app/output) — create it host-side so
	// the bind lands on a dir this user owns.
	if err := os.MkdirAll(filepath.Join(work, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}

	marker := fmt.Sprintf("PROVEO-HELLO-%s-%d", strings.ToUpper(h.target), os.Getpid())
	line := "hello world " + marker
	prompt := fmt.Sprintf(
		"Create a new file at %s whose entire contents are this one line: %s\n"+
			"Do not create, edit or delete any other file. When the file exists, stop.",
		h.promptPath, line)

	sess := tmux.New(fmt.Sprintf("proveo-hello-%s-%d", h.target, os.Getpid()), nil)
	t.Cleanup(sess.Kill)

	cmd := []string{"env"}
	cmd = append(cmd, childEnvArgs(t)...)
	cmd = append(cmd, proveoBin, "run", h.target, "--egress-mode", mode, "--input", work, "--")
	cmd = append(cmd, h.agentArgs(prompt)...)

	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start tmux session: %v", err)
	}

	deadline := time.Now().Add(durationEnv(t, "PROVEO_TEST_TIMEOUT", 8*time.Minute))
	var lastScreen, models string
	var found string
	for {
		screen, captureErr := sess.CaptureAll()
		alive := captureErr == nil
		if alive {
			lastScreen = screen
			if models == "" {
				models = modelsLine.FindString(screen)
			}
		}
		if found = firstExisting(work, h.hostPaths); found != "" {
			break
		}
		if !alive {
			break // the one-shot agent exited; last file check above was the verdict
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(3 * time.Second)
	}

	if s, ok := waitSessionExit(sess, 45*time.Second); ok && s != "" {
		lastScreen = s
	}

	// Models first: a wrong tier is a distinct failure from a missing file, and
	// it is knowable even when the agent never gets around to writing anything.
	assertModels(t, h, models, lastScreen)
	assertCleanBoot(t, lastScreen)

	if found == "" {
		t.Fatalf("no hello-world file on the host after the run\n"+
			"  looked for: %v (under %s)\n--- screen (tail) ---\n%s",
			h.hostPaths, work, tail(lastScreen, 60))
	}
	body := readIn(work, found)
	if !strings.Contains(body, marker) {
		t.Fatalf("%s exists but lacks this run's marker %q\n--- file ---\n%s", found, marker, body)
	}
	t.Logf("bind mount verified: %s contains %q", filepath.Join(work, found), marker)
}

// modelsLine matches the entrypoint preamble every harness prints once the
// model aliases have been bridged into its own env (defs/*/entrypoint.sh).
var modelsLine = regexp.MustCompile(`PROVEO_MODELS main=(\S+) small=(\S+)`)

var bootFailures = []string{
	"Failed to connect to MCP server",
	"MCP tool initialization failed",
	"Traceback (most recent call last)",
	"command not found",
	"cannot create directory",
	"/entrypoint.sh: line",
}

func assertCleanBoot(t *testing.T, screen string) {
	t.Helper()
	for _, sig := range bootFailures {
		if !strings.Contains(screen, sig) {
			continue
		}
		t.Errorf("harness booted degraded — pane contains %q\n"+
			"the side-effect assertion can still pass while a subsystem is dead; fix the harness "+
			"or, if this line is expected, narrow bootFailures\n--- offending lines ---\n%s",
			sig, linesMatching(screen, sig, 3))
	}
}

func waitSessionExit(sess *tmux.Session, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		screen, err := sess.CaptureAll()
		if err != nil {
			return last, true
		}
		last = screen
		time.Sleep(time.Second)
	}
	return last, false
}

func linesMatching(screen, sub string, max int) string {
	var hits []string
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, sub) {
			hits = append(hits, strings.TrimSpace(l))
			if len(hits) == max {
				break
			}
		}
	}
	return strings.Join(hits, "\n")
}

func assertModels(t *testing.T, h helloHarness, models, screen string) {
	t.Helper()
	if models == "" {
		t.Fatalf("harness never printed its PROVEO_MODELS preamble — cannot confirm the "+
			"suggested models reached the container\n--- screen (tail) ---\n%s", tail(screen, 60))
	}
	m := modelsLine.FindStringSubmatch(models)
	gotMain, gotSmall := m[1], m[2]

	wantMain := bareModel(hostEnvValue(t, "ARCHITECT_MODEL"))
	wantSmall := bareModel(hostEnvValue(t, h.smallFrom))

	if wantMain != "" && !strings.Contains(gotMain, wantMain) {
		t.Errorf("main model in container = %q, want it to carry ARCHITECT_MODEL %q", gotMain, wantMain)
	}
	if wantSmall != "" && !strings.Contains(gotSmall, wantSmall) {
		t.Errorf("small model in container = %q, want it to carry %s %q", gotSmall, h.smallFrom, wantSmall)
	}
	t.Logf("models bridged: main=%s small=%s", gotMain, gotSmall)
}

// childEnvArgs builds the `env ...` prefix for the tmux command: every provider
// key and model alias is unset so the filtered file below is the ONLY source of
// truth, then the non-secret switches that keep `proveo run` non-interactive.
// Secrets stay in the file (0600) and never appear on an argv or in `ps`.
func childEnvArgs(t *testing.T) []string {
	t.Helper()
	var args []string
	// DetectVars (superset of KeyVars) matters: a second DETECTED provider — even
	// one that cannot be brokered, like AWS_ACCESS_KEY_ID — makes brokerProvider
	// return "" and the agent then runs with a sentinel and nothing behind it,
	// which surfaces as an indistinguishable "API key is invalid".
	unset := append([]string{"CLAUDE_CODE_OAUTH_TOKEN"}, provider.DetectVars()...)
	unset = append(unset, provider.KeyVars()...)
	unset = append(unset, entrypoint.ConfigVars...)
	for _, k := range unset {
		args = append(args, "-u", k)
	}
	return append(args,
		"PROVEO_EGRESS_ENV_FILE="+writeAgentEnvFile(t),
		"PROVEO_WIZARD=off",       // no scope / capability pickers on this PTY
		"PROVEO_AUTO_PROVISION=1", // build a missing sidecar image instead of asking
		"PROVEO_DIND=0",
	)
}

// writeAgentEnvFile materializes a single-provider .env for the run: the
// Anthropic key plus the non-secret model/UI aliases, nothing else.
func writeAgentEnvFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(agentEnvBody(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func agentEnvBody(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, k := range append([]string{"ANTHROPIC_API_KEY"}, entrypoint.ConfigVars...) {
		if v := hostEnvValue(t, k); v != "" {
			fmt.Fprintf(&b, "%s=%s\n", k, v)
		}
	}
	return b.String()
}

// replaceSampleEnv rewrites the sample's .env (a symlink in the tracked tree,
// dangling once copied) as a real single-provider file inside the workspace.
func replaceSampleEnv(t *testing.T, work string) {
	t.Helper()
	path := filepath.Join(work, ".env")
	if _, err := os.Lstat(path); err != nil {
		return // sample has no .env — nothing to fix up
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(agentEnvBody(t)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// hostEnvValue resolves a key from the test's own environment, falling back to
// the repo's .env (which is how these values are supplied on a dev box).
func hostEnvValue(t *testing.T, key string) string {
	t.Helper()
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return repoEnv(t)[key]
}

var repoEnvCache map[string]string

func repoEnv(t *testing.T) map[string]string {
	t.Helper()
	if repoEnvCache != nil {
		return repoEnvCache
	}
	repoEnvCache = map[string]string{}
	wd, err := os.Getwd()
	if err != nil {
		return repoEnvCache
	}
	path := env("PROVEO_TEST_ENV_FILE", filepath.Join(wd, "..", "..", ".env"))
	b, err := os.ReadFile(path) // follows the symlink dev boxes use
	if err != nil {
		return repoEnvCache
	}
	for _, raw := range strings.Split(string(b), "\n") {
		l := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "export "))
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok {
			repoEnvCache[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return repoEnvCache
}

// bareModel drops a provider prefix ("anthropic/claude-opus-5" -> "claude-opus-5")
// so one expectation matches every harness's id style.
func bareModel(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func firstExisting(dir string, rel []string) string {
	for _, r := range rel {
		if _, err := os.Stat(filepath.Join(dir, r)); err == nil {
			return r
		}
	}
	return ""
}

func durationEnv(t *testing.T, key string, def time.Duration) time.Duration {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		t.Fatalf("%s=%q: %v", key, v, err)
	}
	return d
}

func tail(s string, lines int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}
