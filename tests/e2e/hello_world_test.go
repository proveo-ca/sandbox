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

// TestHelloWorldE2E drives each harness against a COPY of tests/e2e/samples on a
// LOCAL open-source model and asserts three observable facts per target:
//
//	the model answered → the run's transcript carries a model message about the
//	                     work, which is the only proof the inference loop closed
//	                     (skipped for a harness that writes none — see
//	                     repliesOnStdout)
//	bind mount works   → the hello-world file the agent was asked for shows up on
//	                     the HOST, with the run's marker inside it
//	models bridged     → the entrypoint's PROVEO_MODELS line names the local
//	                     model in BOTH tiers, not the harness's baked-in default
//
// Targets run in dependency-of-confidence order: cecli (simplest loop), then
// opencode, then claudecode.
//
//	[PROVEO_TEST_LOCAL_MODEL=gemma4] \
//	  go test -tags=e2e ./tests/e2e/ -run HelloWorldE2E -v -timeout 40m
//
// It runs on a local model on purpose. What this suite asserts — mounts, model
// bridging, a closed inference loop — needs A model, not a particular vendor's,
// and billing it to a cloud provider made the whole lane hostage to a credit
// balance: an empty one is indistinguishable from a broken mount, since both read
// as "no file on the host". No provider credential is supplied at all, so the run
// is provably credit-free rather than merely cheap. Whether a REAL credential
// still reaches a real provider is a different question, and TestClaudeCodeAuth
// asks it against an endpoint that costs nothing.
func TestHelloWorldE2E(t *testing.T) {
	requireTmux(t)
	requireDocker(t)
	model := env("PROVEO_TEST_LOCAL_MODEL", "gemma4")
	if !ollamaHasModel(model) {
		t.Skipf("Ollama model %q not available on the host", model)
	}

	proveoBin := buildProveo(t)

	for _, h := range helloHarnesses {
		t.Run(h.target, func(t *testing.T) {
			harnessImage(t, h.target)
			runHelloWorld(t, h, proveoBin, model)
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
	// repliesOnStdout says whether this harness puts the model's final message on
	// stdout in its one-shot form. Where it does not, nothing the test can read
	// carries the answer, and only the model-authored FILE remains as evidence.
	//
	// opencode is the one that does not: `opencode run` prints its launch line and
	// then NOTHING — not the reply, and not even its own logs despite
	// "--log-level DEBUG --print-logs". Confirmed twice over, from the tmux pane and
	// from a teed transcript of the whole run.
	repliesOnStdout bool
}

var helloHarnesses = []helloHarness{
	{
		target:          "cecli",
		agentArgs:       func(p string) []string { return []string{"--yes-always", "--message", p} },
		promptPath:      "HELLO_WORLD.txt",
		hostPaths:       []string{"HELLO_WORLD.txt"},
		repliesOnStdout: true,
	},
	{
		target:     "opencode",
		agentArgs:  func(p string) []string { return []string{"run", "--auto", "--agent", "build", p} },
		promptPath: "HELLO_WORLD.txt",
		hostPaths:  []string{"HELLO_WORLD.txt"},
		// repliesOnStdout stays false — see the field comment.
	},
	{
		// input-output layout: /workspace itself is not a mount, so the prompt
		// names the writable one explicitly.
		target:          "claudecode",
		agentArgs:       func(p string) []string { return []string{"-p", p} },
		promptPath:      "/workspace/output/HELLO_WORLD.txt",
		hostPaths:       []string{"reports/HELLO_WORLD.txt", "HELLO_WORLD.txt"},
		repliesOnStdout: true,
	},
}

func runHelloWorld(t *testing.T, h helloHarness, proveoBin, model string) {
	t.Helper()

	work := copySampleWorkspace(t)
	// The workspace must carry no project .env: samples/.env is a symlink into the
	// repo root, so `cp -a` leaves a DANGLING link that breaks the bind mount this
	// posture makes of it — and a REAL one would be worse, because the entrypoint
	// sources it AFTER docker applies `-e` and its ARCHITECT_MODEL would outrank
	// the local alias, sending the main agent to a cloud provider.
	removeWorkspaceEnv(t, work)
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
	reply := fmt.Sprintf("PROVEO-REPLY-%s-%d", strings.ToUpper(h.target), os.Getpid())
	prompt := fmt.Sprintf(
		"Create a new file at %s whose entire contents are this one line: %s\n"+
			"Do not create, edit or delete any other file.\n"+
			"When the file exists, reply with exactly %s and stop.",
		h.promptPath, line, reply)

	sess := tmux.New(fmt.Sprintf("proveo-hello-%s-%d", h.target, os.Getpid()), nil)
	t.Cleanup(sess.Kill)

	cmd := []string{"env"}
	cmd = append(cmd, childEnvArgsNoCredential(t)...)
	// `open` + `forward` is the plain-bridge posture, and it is what reaches the
	// HOST's Ollama (host.docker.internal). Every other tier puts the agent on an
	// internal network with DNS blackholed, where the only model server it can see
	// is the sidecar — which on macOS has no GPU and generates at CPU speed.
	cmd = append(cmd, proveoBin, "run", h.target,
		"--egress-mode", "open", "--credentials", "forward",
		"--local-model", model, "--input", work, "--")
	cmd = append(cmd, h.agentArgs(prompt)...)

	// tmux drives a SHELL so the whole run is teed to a file, and every assertion
	// reads that file rather than the pane. The pane cannot hold the model's last
	// words: the agent prints them and exits inside a single poll interval, and
	// once the session is gone CaptureAll can only return what was captured
	// before it died — so the reply was always lost even when the model had said
	// it. The transcript has no such race, and no tmux line wrapping either.
	transcript := filepath.Join(t.TempDir(), "run.log")
	if err := sess.Start(220, 50, "sh", "-c",
		shellQuote(cmd)+" 2>&1 | tee "+shellQuote([]string{transcript})); err != nil {
		t.Fatalf("start tmux session: %v", err)
	}

	deadline := time.Now().Add(durationEnv(t, "PROVEO_TEST_TIMEOUT", 8*time.Minute))
	var models, found string
	var answered bool
	observe := func() {
		out := readFile(transcript)
		if models == "" {
			models = modelsLine.FindString(out)
		}
		answered = answered || modelAnswered(out, prompt, reply, h)
		if found == "" {
			found = firstExisting(work, h.hostPaths)
		}
	}
	for {
		observe()
		// Both halves where both are readable: the agent writes before it answers,
		// so stopping at the file would cut the turn off mid-reply and report a
		// silent model that had simply not finished talking yet. Where the harness
		// writes no message at all there is no second half to wait for, and holding
		// on would just burn the whole deadline.
		if found != "" && (answered || !h.repliesOnStdout) {
			break
		}
		if _, err := sess.CaptureAll(); err != nil {
			observe() // the one-shot agent exited; the transcript outlives it
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(3 * time.Second)
	}
	// Let the run end itself where it still can: killing the pane early SIGHUPs
	// proveo mid-run and strands the egress sidecars and their networks.
	waitSessionExit(sess, 45*time.Second)
	observe()
	out := readFile(transcript)

	// Models first: a wrong tier is a distinct failure from a missing file, and
	// it is knowable even when the agent never gets around to writing anything.
	assertModels(t, model, models, out)
	assertCleanBoot(t, out)

	if h.repliesOnStdout && !answered {
		t.Errorf("the agent surfaced no model answer — neither the reply %q the prompt "+
			"asked for nor any mention of %s. The file alone cannot show the inference "+
			"loop closed, only that something wrote it\n--- transcript (tail) ---\n%s",
			reply, filepath.Base(h.promptPath), tail(out, 40))
	}
	if found == "" {
		t.Fatalf("no hello-world file on the host after the run\n"+
			"  looked for: %v (under %s)\n--- transcript (tail) ---\n%s",
			h.hostPaths, work, tail(out, 60))
	}
	body := readIn(work, found)
	if !strings.Contains(body, marker) {
		t.Fatalf("%s exists but lacks this run's marker %q\n--- file ---\n%s", found, marker, body)
	}
	switch {
	case answered:
		t.Logf("model answered %q", reply)
	case !h.repliesOnStdout:
		t.Logf("%s writes no model message to stdout, so the file is the only evidence "+
			"of the answer here", h.target)
	}
	t.Logf("bind mount verified: %s contains %q", filepath.Join(work, found), marker)
}

// readFile returns path's contents, or "" if it does not exist yet.
func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// shellQuote renders args as one single-quoted shell word list, so a prompt
// carrying spaces and newlines survives the `sh -c` hop intact.
func shellQuote(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(out, " ")
}

// modelAnswered reports whether the model said anything of its own about the work.
//
// It accepts the exact reply the prompt asked for OR a mention of the file the
// model was told to create, and the tolerance is not laziness: a small local model
// reliably reproduces a long random token when COPYING it into a tool call (the
// marker lands in the file byte-exact) yet answers the human in its own words —
// gemma4 replies "File /workspace/output/HELLO_WORLD.txt created successfully."
// rather than echoing the token. Demanding the echo would test instruction-format
// compliance, which is the model's business, instead of whether the loop closed,
// which is proveo's.
func modelAnswered(out, prompt, reply string, h helloHarness) bool {
	return modelSaid(out, prompt, reply) ||
		modelSaid(out, prompt, filepath.Base(h.promptPath))
}

// modelSaid reports whether token appears on the pane as something the MODEL
// produced rather than something the harness echoed back.
//
// Some entrypoints print the prompt on their launch line (opencode's
// "🚀 Launching: opencode …$*"), so the token is already on the pane before any
// inference has happened — and tmux wraps that line, so a line-anchored match
// cannot separate echo from answer either. Collapsing all whitespace makes
// wrapping invisible, deleting every copy of the instruction removes the echo
// whatever its shape, and only the model can have produced what is left.
func modelSaid(screen, prompt, token string) bool {
	flat := func(s string) string { return strings.Join(strings.Fields(s), "") }
	return strings.Contains(strings.ReplaceAll(flat(screen), flat(prompt), ""), flat(token))
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

// assertModels checks the local model reached BOTH tiers. --local-model outranks
// every alias (_spec/internal/entrypoint/model-alias-bridges.puml), so main and
// small must both name it — a tier still holding a cloud default would mean the
// override only half-applied, and that half would quietly bill a provider.
//
// The comparison is on the bare id because no two harnesses spell it the same
// way: claudecode prints "gemma4", opencode "ollama/gemma4", cecli
// "ollama_chat/gemma4". Asserting the spelling would test the catalog, not the
// bridge.
func assertModels(t *testing.T, model, models, screen string) {
	t.Helper()
	if models == "" {
		t.Fatalf("harness never printed its PROVEO_MODELS preamble — cannot confirm the "+
			"local model reached the container\n--- screen (tail) ---\n%s", tail(screen, 60))
	}
	m := modelsLine.FindStringSubmatch(models)
	gotMain, gotSmall := m[1], m[2]

	want := bareModel(model)
	for _, tier := range []struct{ name, got string }{{"main", gotMain}, {"small", gotSmall}} {
		if bareModel(tier.got) != want {
			t.Errorf("%s model in container = %q, want the local model %q — "+
				"--local-model outranks every alias, so a tier that still holds "+
				"something else means the override only half-applied", tier.name, tier.got, want)
		}
	}
	t.Logf("models bridged: main=%s small=%s", gotMain, gotSmall)
}

// childEnvArgs builds the `env ...` prefix for the tmux command: every provider
// key and model alias is unset so the filtered file below is the ONLY source of
// truth, then the non-secret switches that keep `proveo run` non-interactive.
// Secrets stay in the file (0600) and never appear on an argv or in `ps`.
func childEnvArgs(t *testing.T) []string {
	t.Helper()
	return append(childEnvArgsNoCredential(t), "PROVEO_EGRESS_ENV_FILE="+writeAgentEnvFile(t))
}

// childEnvArgsNoCredential is the same prefix with NO provider credential at all:
// every key is unset and no PROVEO_EGRESS_ENV_FILE is supplied. A run under it
// cannot reach a paid provider even by accident, which is what makes a
// local-model lane provably credit-free instead of merely cheap.
//
// env(1) on BSD/macOS requires every -u before the first NAME=value, so the unset
// flags stay at the front and callers may only append.
func childEnvArgsNoCredential(t *testing.T) []string {
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
