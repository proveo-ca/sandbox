//go:build e2e

// SPEC: _spec/internal/sbx/kit-image-source.puml
package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/imagebuild"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/schedule"
	"github.com/proveo-ca/proveo/internal/termtext"
)

// containerBuilder is a running docker-container buildx builder: one that cannot read the host store.
const containerBuilder = "proveo-multiarch"

// TestAHostOnlyImageBuildsWhileAContainerBuilderIsActive builds FROM an image only the host engine
// holds while the inherited BUILDX_BUILDER names a docker-container builder.
func TestAHostOnlyImageBuildsWhileAContainerBuilderIsActive(t *testing.T) {
	requireSbx(t)
	base := harnessImage(t, "codex")
	if out, err := exec.Command("docker", "buildx", "inspect", containerBuilder).CombinedOutput(); err != nil ||
		!strings.Contains(string(out), "docker-container") {
		t.Skipf("no %s docker-container builder to stand in for the active one (`mise run deploy` creates it)", containerBuilder)
	}
	hostOnly := fmt.Sprintf("proveo-e2e/hostonly-%d:local", time.Now().UnixNano())
	if out, err := exec.Command("docker", "tag", base, hostOnly).CombinedOutput(); err != nil {
		t.Fatalf("docker tag %s %s: %v\n%s", base, hostOnly, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", hostOnly).Run() })

	src := sbx.ResolveImageSource(hostOnly, imagebuild.New("").HostBuilder)
	if !src.Host {
		t.Fatalf("ResolveImageSource(%q) = %+v; the tag was just made on the host", hostOnly, src)
	}
	if src.Builder == "" {
		t.Skipf("no docker-driver buildx builder on this host reads its store: %+v", src)
	}
	ambient := append(os.Environ(), "BUILDX_BUILDER="+containerBuilder)

	t.Run("the source's builder and digest build it", func(t *testing.T) {
		out, err := createProbe(t, src.From(), sbx.CreateEnv(ambient, sbx.RunConfig{HostEnv: src.BuildEnv()}))
		if err != nil {
			t.Errorf("sbx create FROM %s with HostEnv %q under BUILDX_BUILDER=%s: %v\n%s",
				src.From(), src.BuildEnv(), containerBuilder, err, out)
		}
	})
	if src.Digest == "" {
		return
	}
	t.Run("the pinned digest refuses a builder that cannot see the host store", func(t *testing.T) {
		out, err := createProbe(t, src.From(), ambient)
		if err == nil {
			t.Errorf("sbx create FROM %s built under the container builder alone; want a loud failure, never a silent docker.io image:\n%s",
				src.From(), out)
		}
	})
}

// createProbe runs `sbx create` on a minimal workload FROM from, with env on the sbx process.
func createProbe(t *testing.T, from string, env []string) (string, error) {
	t.Helper()
	sweepSandboxesAfter(t)
	name := fmt.Sprintf("probe-src-%d", time.Now().UnixNano())
	kit, err := sbx.WriteWorkloadV3(filepath.Join(t.TempDir(), name), sbx.WorkloadV3{
		Capabilities: []sbx.CapabilityV3{{Type: sbx.CapSbx}},
		Version:      "0.0.1",
	}, sbx.RecipeV3{From: from, Entrypoint: []string{"bash"}}, nil)
	if err != nil {
		t.Fatalf("WriteWorkloadV3: %v", err)
	}
	t.Cleanup(func() { _, _ = removeSandbox(t, name) })
	c := exec.Command(sbx.Binary, sbx.CreateArgs(sbx.RunConfig{Name: name, KitDir: kit})...)
	c.Env = env
	out, err := c.CombinedOutput()
	return string(out), err
}

// SPEC: _spec/tests/testing-strategy.puml

// kitRun is one `proveo run <target>` through its kit under tmux, and the
// sandbox it runs in.
type kitRun struct {
	t      *testing.T
	sess   *schedule.TmuxSession
	name   string
	dir    string
	last   string
	target string
}

// launchKit starts `proveo run <target>` through its kit under tmux, with the
// picker skipped, credentials forwarded and a private PROVEO_HOME unless env
// names one, and returns once the sandbox's seed has finished. env is set on
// the proveo process; extra follows the run flags.
func launchKit(t *testing.T, proveoBin, target, dir string, env []string, extra ...string) *kitRun {
	t.Helper()
	requireSbx(t)
	if !slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "PROVEO_HOME=") }) {
		env = append(env, "PROVEO_HOME="+t.TempDir())
	}
	sweepSandboxesAfter(t)
	before, _ := sbxSandboxNames()
	sess := schedule.NewTmux(fmt.Sprintf("proveo-kit-%s-%d", target, time.Now().UnixNano()), nil)
	cmd := append([]string{"env", "PROVEO_WIZARD=off", "PROVEO_MOUNT_GH_CONFIG=0"}, env...)
	cmd = append(cmd, proveoBin, "run", target, "--credentials", "forward", "--input", dir)
	if err := sess.Start(220, 50, append(cmd, extra...)...); err != nil {
		t.Fatalf("start proveo run %s: %v", target, err)
	}
	return awaitKit(t, sess, before, target, dir)
}

// awaitKit names the sandbox the `proveo run <target> --input dir` in sess
// created since before, waits for its seed, and at cleanup ends the run and
// removes the sandbox before any directory it mounts.
func awaitKit(t *testing.T, sess *schedule.TmuxSession, before map[string]bool, target, dir string) *kitRun {
	t.Helper()
	pid, _ := sess.PanePID()
	k := &kitRun{t: t, sess: sess, dir: dir, target: target}
	t.Cleanup(func() {
		killAndAwait(t, sess, pid, durationEnv(t, "PROVEO_TEST_TEARDOWN", 3*time.Minute))
		if k.name != "" {
			_ = exec.Command(sbx.Binary, "rm", "--force", k.name).Run()
		}
	})
	timeout := durationEnv(t, "PROVEO_TEST_TIMEOUT", 8*time.Minute)
	if !waitUntil(timeout, func() bool { k.tend(); k.name = kitSandbox(before, target, dir); return k.name != "" }) {
		t.Fatalf("no %s sandbox appeared within %s\n%s", target, timeout, tail(k.last, 40))
	}
	k.awaitSeed()
	// wait again once the pane settles
	awaitQuietScreen(sess, 30*time.Second)
	k.awaitSeed()
	return k
}

// awaitSeed holds until the sandbox's seed-done marker exists.
func (k *kitRun) awaitSeed() {
	k.t.Helper()
	seed := durationEnv(k.t, "PROVEO_TEST_SEED_TIMEOUT", 12*time.Minute)
	if !waitUntil(seed, func() bool {
		k.tend()
		_, rc, ok := k.try("test -e "+seedDone, time.Minute)
		return ok && rc == 0
	}) {
		log, _, _ := k.try("tail -40 /var/log/sbx-kit-startup.log", time.Minute)
		k.t.Fatalf("the seed in %s did not finish within %s:\n%s\n%s", k.name, seed, log, tail(k.last, 40))
	}
}

// end stops the run the way `docker stop` or a closed terminal does: SIGTERM
// to proveo, whose own teardown then runs. It returns the last screen and
// whether proveo exited within the teardown budget.
func (k *kitRun) end() (string, bool) {
	k.t.Helper()
	return stopRun(k.sess, durationEnv(k.t, "PROVEO_TEST_TEARDOWN", 3*time.Minute))
}

// stopRun sends SIGTERM to the proveo in sess and returns its last screen and
// whether it exited within timeout; past timeout it SIGKILLs proveo and kills the session.
func stopRun(sess *schedule.TmuxSession, timeout time.Duration) (string, bool) {
	pid, _ := sess.PanePID()
	var proc *os.Process
	if n, err := strconv.Atoi(strings.TrimSpace(pid)); err == nil && n > 0 {
		proc, _ = os.FindProcess(n)
	}
	if proc != nil {
		_ = proc.Signal(syscall.SIGTERM)
	}
	screen, exited := waitSessionExit(sess, timeout)
	if !exited {
		if proc != nil {
			_ = proc.Kill()
		}
		sess.Kill()
	}
	return screen, exited
}

// tend answers sbx's own operator questions, fails on a refusal or on a
// confirmation that would swallow input, and handles a run that already ended.
func (k *kitRun) tend() {
	k.t.Helper()
	screen, err := k.sess.CaptureAll()
	if err != nil {
		k.ended()
	}
	k.last = screen
	if services, ok := consentPrompt(screen); ok {
		k.t.Fatalf("%s\n%s", consentFailure(services, k.target), tail(screen, 20))
	}
	if line := sbxError(screen); line != "" && !retryable(line) {
		k.t.Fatalf("sbx refused the run — %s\n%s", line, tail(screen, 40))
	}
	if line := brokenPrompt(screen); line != "" {
		k.t.Fatalf("a confirmation is eating input meant for something else — %s\n%s", line, tail(screen, 40))
	}
}

// ended skips when the agent quit for want of a login and fails otherwise.
func (k *kitRun) ended() {
	k.t.Helper()
	final := termtext.Words(tail(k.last, 15))
	for _, f := range authFailures {
		if strings.Contains(final, f) {
			k.t.Skipf("proveo exited before the sandbox was ready: the agent is not authenticated (%q)", f)
		}
	}
	k.t.Fatalf("proveo exited before the sandbox was ready\n%s", tail(k.last, 40))
}

// with is k bound to t, for subtests that share one run.
func (k *kitRun) with(t *testing.T) *kitRun {
	c := *k
	c.t = t
	return &c
}

// exec runs script under bash in the sandbox as the agent's user, in the
// agent's working directory with the provisioned toolchain on PATH, and
// returns its combined output and exit status.
func (k *kitRun) exec(script string, timeout time.Duration) (string, int) {
	k.t.Helper()
	prelude := "launch_dir=" + shellQuote([]string{k.dir}) + "\n" +
		`cd "${PROVEO_WORKDIR:-$launch_dir}" 2>/dev/null || cd "$launch_dir" || ` +
		`{ echo "kit probe: cannot enter $launch_dir" >&2; exit 97; }` + "\n" +
		`[ -f "$HOME/.proveo-tool-env.sh" ] && . "$HOME/.proveo-tool-env.sh"` + "\n"
	return k.raw(prelude+script, timeout)
}

// raw runs script under bash in the sandbox from /, with nothing sourced.
func (k *kitRun) raw(script string, timeout time.Duration) (string, int) {
	k.t.Helper()
	return sbxExecStatus(k.t, k.name, script, timeout)
}

// try is raw without failing the test; ok is false when sbx exec itself failed or timed out.
func (k *kitRun) try(script string, timeout time.Duration) (string, int, bool) {
	ctx, cancel := context.WithTimeout(k.t.Context(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sbx.Binary, "exec", "-w", "/", k.name, "--", "bash", "-c", script)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0, true
	case errors.As(err, &exit) && ctx.Err() == nil:
		return string(out), exit.ExitCode(), true
	}
	return string(out), -1, false
}

// pane is the last n lines proveo and the agent printed.
func (k *kitRun) pane(n int) string {
	if screen, err := k.sess.CaptureAll(); err == nil {
		k.last = screen
	}
	return tail(k.last, n)
}

// kitSandbox names the sandbox created since before with target's name prefix,
// its primary workspace under the test temp root, and a workspace holding dir.
func kitSandbox(before map[string]bool, target, dir string) string {
	out, err := exec.Command(sbx.Binary, "ls", "--json").Output()
	if err != nil {
		return ""
	}
	return pickKitSandbox(out, before, target, kitDirs(dir))
}

// kitDirs is dir as sbx may list it: as given, with symlinks resolved, and as
// the main worktree a linked worktree's clone mounts instead.
func kitDirs(dir string) []string {
	dirs := []string{filepath.Clean(dir)}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dirs[0] {
		dirs = append(dirs, real)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--git-common-dir").Output()
	common := strings.TrimSpace(string(out))
	if err != nil || filepath.Base(common) != ".git" {
		return dirs
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	if main := filepath.Dir(filepath.Clean(common)); !pathWithin(dir, main) {
		dirs = append(dirs, main)
	}
	return dirs
}

// pickKitSandbox is kitSandbox over an `sbx ls --json` listing.
func pickKitSandbox(listing []byte, before map[string]bool, target string, dirs []string) string {
	parsed, err := sbxListing(listing)
	if err != nil {
		return ""
	}
	prefix := sbx.SandboxName(target, "") + "-"
	for _, sb := range parsed {
		if before[sb.Name] || !strings.HasPrefix(sb.Name, prefix) || !testOwned(sb.Workspaces) {
			continue
		}
		for _, ws := range sb.Workspaces {
			ws = workspacePath(ws)
			if slices.ContainsFunc(dirs, func(d string) bool { return pathWithin(d, ws) || pathWithin(ws, d) }) {
				return sb.Name
			}
		}
	}
	return ""
}

// testOwned reports whether the primary workspace lies strictly below a test temp root.
func testOwned(workspaces []string) bool {
	if len(workspaces) == 0 {
		return false
	}
	primary := workspacePath(workspaces[0])
	return underTestTemp(primary) && !slices.Contains(testTempRoots(), primary)
}

// workspacePath is a listed workspace without its read-only suffix.
func workspacePath(ws string) string {
	return filepath.Clean(strings.TrimSuffix(ws, ":ro"))
}

// pathWithin reports whether p is dir or lies under it.
func pathWithin(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func TestKitSandboxNamesOnlyTheRunsOwnSandbox(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	work := filepath.Join(tmp, "work")
	main := filepath.Join(tmp, "main")
	state := "/operator/.local/state/proveo/runs/proveo-1-2/sbx/home-files"
	operator := "/operator/Projects/repo"

	type sandbox struct {
		Name       string   `json:"name"`
		Workspaces []string `json:"workspaces"`
	}
	cases := []struct {
		name   string
		before []string
		dirs   []string
		listed []sandbox
		want   string
	}{
		{"a home-files workspace outside temp does not disown it", nil, []string{work},
			[]sandbox{{"proveo-claudecode-1a2b3c4d", []string{work, state + ":ro"}}}, "proveo-claudecode-1a2b3c4d"},
		{"a read-only primary workspace still holds dir", nil, []string{work},
			[]sandbox{{"proveo-claudecode-1a2b3c4d", []string{work + ":ro"}}}, "proveo-claudecode-1a2b3c4d"},
		{"an operator's sandbox is never named", nil, []string{work},
			[]sandbox{{"proveo-claudecode-9f9f9f9f", []string{operator, work}}}, ""},
		{"a sandbox on the temp root itself is never named", nil, []string{work},
			[]sandbox{{"proveo-claudecode-9f9f9f9f", []string{os.TempDir()}}}, ""},
		{"a sandbox that existed before the run is never named", []string{"proveo-claudecode-1a2b3c4d"}, []string{work},
			[]sandbox{{"proveo-claudecode-1a2b3c4d", []string{work}}}, ""},
		{"another target's sandbox is never named", nil, []string{work},
			[]sandbox{{"proveo-codex-1a2b3c4d", []string{work}}}, ""},
		{"a linked worktree's clone is found through its main worktree", nil, []string{work, main},
			[]sandbox{
				{"proveo-claudecode-0000aaaa", []string{filepath.Join(tmp, "other")}},
				{"proveo-claudecode-1a2b3c4d", []string{main}},
			}, "proveo-claudecode-1a2b3c4d"},
		{"a new test sandbox that holds no dir of this run is never named", nil, []string{work},
			[]sandbox{{"proveo-claudecode-1a2b3c4d", []string{filepath.Join(tmp, "other")}}}, ""},
		{"a scoped --input under the sandbox's workspace is held", nil, []string{filepath.Join(work, "apps")},
			[]sandbox{{"proveo-claudecode-1a2b3c4d", []string{work}}}, "proveo-claudecode-1a2b3c4d"},
		{"two new test sandboxes, neither holding dir, name none", nil, []string{work},
			[]sandbox{
				{"proveo-claudecode-0000aaaa", []string{filepath.Join(tmp, "a")}},
				{"proveo-claudecode-1a2b3c4d", []string{filepath.Join(tmp, "b")}},
			}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			before := map[string]bool{}
			for _, n := range c.before {
				before[n] = true
			}
			for _, shape := range []any{c.listed, map[string][]sandbox{"sandboxes": c.listed}} {
				listing, err := json.Marshal(shape)
				if err != nil {
					t.Fatal(err)
				}
				if got := pickKitSandbox(listing, before, "claudecode", c.dirs); got != c.want {
					t.Errorf("pickKitSandbox(%s) = %q, want %q", listing, got, c.want)
				}
			}
		})
	}
}

func TestKitDirsAddsOnlyALinkedWorktreesMain(t *testing.T) {
	t.Parallel()
	gitHome := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env, "HOME="+gitHome, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	base := t.TempDir()
	main := filepath.Join(base, "main")
	if err := os.MkdirAll(filepath.Join(main, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(main, "init", "-q", ".")
	git(main, "commit", "-q", "--allow-empty", "-m", "seed")
	wt := filepath.Join(base, "wt")
	git(main, "worktree", "add", "-q", wt)

	resolved := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	if got := kitDirs(wt); !slices.ContainsFunc(got, func(d string) bool { return resolved(d) == resolved(main) }) {
		t.Errorf("kitDirs(%s) = %v, want it to name the main worktree %s its clone mounts", wt, got, main)
	}
	for _, dir := range []string{main, filepath.Join(main, "apps")} {
		for _, d := range kitDirs(dir) {
			if resolved(d) != resolved(dir) {
				t.Errorf("kitDirs(%s) added %s; only a linked worktree's main belongs there — a checkout root would hold every test sandbox under it", dir, d)
			}
		}
	}
}

func TestASbxConsentPromptNamesItsServices(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		screen string
		want   string
		wantOK bool
	}{
		{"sbx's frame with two services", "Starting sandbox\nThis kit wants to use these credentials:\n  opencode     API key → sent to .opencode.ai   (not set)\n  anthropic    API key → sent to .anthropic.com\n[A]pprove all · [R]eview each · [N]o:\n", "opencode, anthropic", true},
		{"a bare prompt", "[A]pprove all · [R]eview each · [N]o:", "its credentials", true},
		{"sbx 0.49's numbered frame", "This kit wants to use these credentials:\n  openai: API key sent to api.openai.com, openai.com (not set)\n    OAuth refreshed at auth.openai.com; used at chatgpt.com\n  xai: API key sent to api.x.ai (stored)\nApprove these credentials?\n  1. Approve all\n  2. Review each\n  3. No start without them\nChoice [1-3, default 3]:", "openai, xai", true},
		{"no prompt", "agent: ready\n", "", false},
	} {
		got, ok := consentPrompt(c.screen)
		if got != c.want || ok != c.wantOK {
			t.Errorf("consentPrompt(%s) = %q, %v; want %q, %v", c.name, got, ok, c.want, c.wantOK)
		}
	}
	if msg := consentFailure("opencode", "opencode"); !strings.Contains(msg, "approve opencode") || !strings.Contains(msg, "interactive `proveo run opencode`") {
		t.Errorf("consentFailure = %q, want it to name the service and the one-time interactive run", msg)
	}
}
