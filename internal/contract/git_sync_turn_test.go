// SPEC: _spec/packages/lib/git-sync-turn.puml
package contract_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func gitSyncScript(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "packages", "lib", "hooks", "git-sync-turn.sh")
}

func gitSyncPlugin(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "packages", "lib", "hooks", "proveo-git-sync-turn.js")
}

func gitOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
}

func gitConfigHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(cfg, []byte("[user]\n\tname = proveo-test\n\temail = proveo@test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func gitCmd(t *testing.T, dir string, extraEnv []string, args ...string) string {
	t.Helper()
	gitOrSkip(t)
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + gitConfigHome(t),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=proveo-test",
		"GIT_AUTHOR_EMAIL=proveo@test",
		"GIT_COMMITTER_NAME=proveo-test",
		"GIT_COMMITTER_EMAIL=proveo@test",
	}, extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCmd(t, dir, nil, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, nil, "add", "README")
	gitCmd(t, dir, nil, "commit", "-m", "init")
	return dir
}

// hookEnv defaults PROVEO_GIT_SYNC_MSG=off: the test machine's own PATH
// carries a real `claude` binary (this suite runs inside a claudecode
// sandbox), so leaving message generation on would fire a live model call
// per dirty-tree test. Tests exercising generation pass their own
// PROVEO_GIT_SYNC_MSG=auto as extra, which overrides the default by key.
func hookEnv(t *testing.T, extra ...string) []string {
	t.Helper()
	base := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + gitConfigHome(t),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=proveo-test",
		"GIT_AUTHOR_EMAIL=proveo@test",
		"GIT_COMMITTER_NAME=proveo-test",
		"GIT_COMMITTER_EMAIL=proveo@test",
		"PROVEO_GIT_SYNC_DIALECT=stop",
		"PROVEO_GIT_SYNC_MSG=off",
	}
	vals := map[string]string{}
	var order []string
	set := func(kv string) {
		k, v, _ := strings.Cut(kv, "=")
		if _, ok := vals[k]; !ok {
			order = append(order, k)
		}
		vals[k] = v
	}
	for _, kv := range base {
		set(kv)
	}
	for _, kv := range extra {
		set(kv)
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+vals[k])
	}
	return out
}

// stubModelCLI writes an executable named `name` (e.g. "claude") into a
// fresh dir and returns that dir, for callers to prepend onto PATH so
// git-sync-turn.sh's `command -v` finds the stub before any real CLI.
func stubModelCLI(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runGitSync(t *testing.T, dir, stdin string, env []string) (string, string, int) {
	t.Helper()
	bash := bashOrSkip(t)
	cmd := exec.Command(bash, gitSyncScript(t))
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = env
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run git-sync-turn: %v\n%s", err, stderr.String())
		}
	}
	return stdout.String(), stderr.String(), code
}

func TestGitSyncAllowsACleanTree(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	head := strings.TrimSpace(gitCmd(t, dir, nil, "rev-parse", "HEAD"))
	out, _, code := runGitSync(t, dir, `{"hook_event_name":"Stop"}`, hookEnv(t))
	if code != 0 {
		t.Fatalf("exit %d, want 0: %s", code, out)
	}
	if out != "" {
		t.Errorf("stdout %q, want empty: Stop accepts only \"block\" or no decision", out)
	}
	if got := strings.TrimSpace(gitCmd(t, dir, nil, "rev-parse", "HEAD")); got != head {
		t.Errorf("HEAD moved on a clean tree: %s -> %s", head, got)
	}
}

func TestGitSyncCommitsADirtyTree(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errb, code := runGitSync(t, dir, `{"hook_event_name":"Stop"}`, hookEnv(t))
	if code != 0 {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errb)
	}
	if out != "" {
		t.Errorf("stdout %q, want empty: Stop accepts only \"block\" or no decision", out)
	}
	sub := gitCmd(t, dir, nil, "log", "-1", "--pretty=%s")
	if !strings.Contains(sub, "[proveo] persist turn") {
		t.Errorf("subject %q, want [proveo] persist turn", sub)
	}
	if _, err := os.Stat(filepath.Join(dir, "work.txt")); err != nil {
		t.Fatal(err)
	}
	tracked := gitCmd(t, dir, nil, "ls-files", "work.txt")
	if !strings.Contains(tracked, "work.txt") {
		t.Errorf("work.txt was not committed: %s", tracked)
	}
}

func TestGitSyncGeneratesCommitSubjectFromTheModel(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "inflight-seen")
	stub := stubModelCLI(t, "claude", fmt.Sprintf(`
[[ "$PROVEO_GIT_SYNC_MSG_INFLIGHT" == 1 ]] && touch '%s'
echo "add the missing feature flag"
`, marker))
	env := hookEnv(t,
		"PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PROVEO_GIT_SYNC_MSG=auto",
	)
	out, errb, code := runGitSync(t, dir, `{"hook_event_name":"Stop"}`, env)
	if code != 0 {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errb)
	}
	sub := strings.TrimSpace(gitCmd(t, dir, nil, "log", "-1", "--pretty=%s"))
	if sub != "[proveo] add the missing feature flag" {
		t.Errorf("subject %q, want the stubbed model's line under the [proveo] tag", sub)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("the stubbed model never saw PROVEO_GIT_SYNC_MSG_INFLIGHT=1 — a Stop hook fired from inside it would recurse")
	}
}

func TestGitSyncFallsBackWhenTheModelFails(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "flaky.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := stubModelCLI(t, "claude", `exit 1`)
	env := hookEnv(t,
		"PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PROVEO_GIT_SYNC_MSG=auto",
	)
	out, errb, code := runGitSync(t, dir, `{"hook_event_name":"Stop"}`, env)
	if code != 0 {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errb)
	}
	sub := gitCmd(t, dir, nil, "log", "-1", "--pretty=%s")
	if !strings.Contains(sub, "[proveo] persist turn") {
		t.Errorf("subject %q, want the static fallback when the model errors", sub)
	}
}

func TestGitSyncMsgOffSkipsTheModelEvenWhenOneIsAvailable(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "quiet.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := stubModelCLI(t, "claude", `echo "should never be used"`)
	env := hookEnv(t,
		"PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PROVEO_GIT_SYNC_MSG=off",
	)
	_, _, code := runGitSync(t, dir, `{"hook_event_name":"Stop"}`, env)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	sub := gitCmd(t, dir, nil, "log", "-1", "--pretty=%s")
	if !strings.Contains(sub, "[proveo] persist turn") {
		t.Errorf("subject %q, PROVEO_GIT_SYNC_MSG=off must keep the static subject", sub)
	}
}

func TestGitSyncMsgInflightGuardIsANoOp(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "recursive.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errb, code := runGitSync(t, dir, `{"hook_event_name":"Stop"}`, hookEnv(t, "PROVEO_GIT_SYNC_MSG_INFLIGHT=1"))
	if code != 0 || out != "" || errb != "" {
		t.Fatalf("inflight guard: exit %d stdout %q stderr %q, want a silent exit 0", code, out, errb)
	}
	st := gitCmd(t, dir, nil, "status", "--porcelain")
	if !strings.Contains(st, "recursive.txt") {
		t.Error("a nested invocation must not commit — that is the guard against a Stop hook triggering itself")
	}
}

func TestGitSyncGuardsAgainstRecursiveModelInvocation(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "packages/lib/hooks/git-sync-turn.sh")
	if !strings.Contains(src, "PROVEO_GIT_SYNC_MSG_INFLIGHT") {
		t.Fatal("git-sync-turn.sh lacks a PROVEO_GIT_SYNC_MSG_INFLIGHT guard — a nested claude/codex/cursor-agent/opencode call can itself fire this same Stop hook")
	}
	if strings.Index(src, "PROVEO_GIT_SYNC_MSG_INFLIGHT") > strings.Index(src, "_json_get()") {
		t.Error("the inflight guard must short-circuit before any hook logic runs, not just inside the subject generator")
	}
}

func TestGitSyncLeavesSecretFilesUncommitted(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errb, code := runGitSync(t, dir, `{"hook_event_name":"Stop"}`, hookEnv(t))
	if code != 0 {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errb)
	}
	files := gitCmd(t, dir, nil, "ls-files")
	if strings.Contains(files, ".env") {
		t.Errorf(".env was committed:\n%s", files)
	}
	if !strings.Contains(files, "visible.txt") {
		t.Errorf("visible.txt was not committed:\n%s", files)
	}
}

func TestGitSyncOffLeavesTheTreeDirty(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "skip.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, code := runGitSync(t, dir, `{"hook_event_name":"Stop"}`, hookEnv(t, "PROVEO_GIT_SYNC=off"))
	if code != 0 {
		t.Fatalf("exit %d stdout %q", code, out)
	}
	st := gitCmd(t, dir, nil, "status", "--porcelain")
	if !strings.Contains(st, "skip.txt") {
		t.Errorf("off must not commit; status %q", st)
	}
}

func TestGitSyncAllowsANonRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, _, code := runGitSync(t, dir, `{"hook_event_name":"Stop"}`, hookEnv(t))
	if code != 0 || out != "" {
		t.Errorf("non-repo: exit %d stdout %q, want exit 0 and no decision", code, out)
	}
}

func TestGitSyncPushesToOrigin(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	bare := t.TempDir()
	gitCmd(t, bare, nil, "init", "--bare", "-b", "main")
	gitCmd(t, dir, nil, "remote", "add", "origin", bare)
	gitCmd(t, dir, nil, "push", "-u", "origin", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "pushed.txt"), []byte("p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errb, code := runGitSync(t, dir, `{"hook_event_name":"Stop"}`, hookEnv(t))
	if code != 0 {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errb)
	}
	remoteLog := gitCmd(t, bare, nil, "log", "-1", "--pretty=%s")
	if !strings.Contains(remoteLog, "[proveo] persist turn") {
		t.Errorf("origin log %q, want the persist commit", remoteLog)
	}
}

func TestGitSyncBlocksWhenCommitHasNoIdentity(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "noid.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(home, "empty"),
		// A hostname with a domain (macOS's *.local) lets git invent an identity.
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.useConfigOnly", "GIT_CONFIG_VALUE_0=true",
		"PROVEO_GIT_SYNC_DIALECT=stop",
		"PROVEO_GIT_SYNC_MSG=off",
	}
	if err := os.WriteFile(filepath.Join(home, "empty"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, code := runGitSync(t, dir, `{"hook_event_name":"Stop","stop_hook_active":false}`, env)
	if code != 0 {
		t.Fatalf("exit %d stdout %q", code, out)
	}
	if !strings.Contains(out, `"decision":"block"`) {
		t.Errorf("stdout %q, want decision block", out)
	}
	st := gitCmd(t, dir, nil, "status", "--porcelain")
	if !strings.Contains(st, "noid.txt") {
		t.Errorf("a failed commit must leave the work: %q", st)
	}
}

func TestGitSyncAllowsAfterAStopContinuation(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "again.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(home, "empty"),
		// A hostname with a domain (macOS's *.local) lets git invent an identity.
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.useConfigOnly", "GIT_CONFIG_VALUE_0=true",
		"PROVEO_GIT_SYNC_DIALECT=stop",
		"PROVEO_GIT_SYNC_MSG=off",
	}
	if err := os.WriteFile(filepath.Join(home, "empty"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, code := runGitSync(t, dir, `{"hook_event_name":"Stop","stop_hook_active":true}`, env)
	if code != 0 {
		t.Fatalf("exit %d stdout %q", code, out)
	}
	if out != "" {
		t.Errorf("stdout %q, want no decision on a continuation so the 8-cap / loop_limit can rest", out)
	}
}

func TestGitSyncCursorFollowupOnFailure(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "cur.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(home, "empty"),
		// A hostname with a domain (macOS's *.local) lets git invent an identity.
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.useConfigOnly", "GIT_CONFIG_VALUE_0=true",
		"PROVEO_GIT_SYNC_DIALECT=cursor",
		"PROVEO_GIT_SYNC_MSG=off",
	}
	if err := os.WriteFile(filepath.Join(home, "empty"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, code := runGitSync(t, dir, `{"status":"completed","loop_count":0}`, env)
	if code != 0 {
		t.Fatalf("exit %d stdout %q", code, out)
	}
	var j struct {
		Followup string `json:"followup_message"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &j); err != nil {
		t.Fatalf("cursor stdout is not JSON: %v\n%s", err, out)
	}
	if j.Followup == "" {
		t.Errorf("cursor failure must follow up, got %s", out)
	}
}

func TestGitSyncSkipsAnAbortedCursorTurn(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "aborted.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, code := runGitSync(t, dir, `{"status":"aborted","loop_count":0}`, hookEnv(t, "PROVEO_GIT_SYNC_DIALECT=cursor"))
	if code != 0 {
		t.Fatalf("exit %d stdout %q", code, out)
	}
	if strings.TrimSpace(out) != "{}" {
		t.Errorf("aborted cursor turn stdout %q, want {}", out)
	}
	st := gitCmd(t, dir, nil, "status", "--porcelain")
	if !strings.Contains(st, "aborted.txt") {
		t.Errorf("aborted turn must not commit: %q", st)
	}
}

func TestGitSyncIdleCommitsWithoutJSON(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "idle.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errb, code := runGitSync(t, dir, "", hookEnv(t, "PROVEO_GIT_SYNC_DIALECT=idle"))
	if code != 0 {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errb)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("idle dialect must not print JSON, got %q", out)
	}
	if !strings.Contains(gitCmd(t, dir, nil, "ls-files"), "idle.txt") {
		t.Error("idle dialect must still commit")
	}
}

func TestGitSyncScriptNeverForcePushesOrSkipsHooks(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "packages/lib/hooks/git-sync-turn.sh")
	// Scoped to lines that actually invoke `git`: cursor-agent's own --force
	// (a headless-trust flag, unrelated to git) legitimately appears on the
	// commit-subject generator's cursor-agent invocation line.
	banned := []string{"--force", "--no-verify", "--amend", "git config "}
	for _, line := range strings.Split(src, "\n") {
		if !strings.Contains(line, "git ") {
			continue
		}
		for _, b := range banned {
			if strings.Contains(line, b) {
				t.Errorf("git-sync-turn.sh git invocation contains %q: %q", b, line)
			}
		}
	}
	for _, want := range []string{"[ ! -t 0 ]", "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(src, want) {
			t.Errorf("git-sync-turn.sh lacks %q — a TTY stdin is the opencode idle freeze", want)
		}
	}
}

func TestGitSyncPluginListensForSessionIdle(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "packages/lib/hooks/proveo-git-sync-turn.js")
	for _, want := range []string{
		"session.idle", "git-sync-turn.sh", "PROVEO_GIT_SYNC_DIALECT=idle",
		"</dev/null", "GIT_TERMINAL_PROMPT=0", ".quiet()",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("plugin lacks %q", want)
		}
	}
	if !strings.Contains(src, "await $") {
		t.Error("an unawaited Bun ShellPromise never spawns, so session.idle runs no hook")
	}
}

func TestGitSyncIdleOnATTYDoesNotBlock(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "idle.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bash := bashOrSkip(t)
	cmd := exec.Command(bash, gitSyncScript(t))
	cmd.Dir = dir
	cmd.Env = hookEnv(t, "PROVEO_GIT_SYNC_DIALECT=idle")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty: %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("idle on a TTY exited %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("git-sync-turn blocked reading the TTY — that is proveo run opencode going deaf at session.idle")
	}
	if !strings.Contains(gitCmd(t, dir, nil, "ls-files"), "idle.txt") {
		t.Error("idle dialect must still commit when stdin is a TTY")
	}
}

func TestCursorEnterpriseHooksWireGitSyncStop(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "defs/cursor/defaults/hooks.json")
	for _, want := range []string{`"stop"`, "git-sync-turn.sh", `"loop_limit": 1`} {
		if !strings.Contains(src, want) {
			t.Errorf("cursor hooks.json lacks %q", want)
		}
	}
}

func TestDockerfilesShipTheGitSyncTurnHook(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{
		"defs/base/Dockerfile",
		"defs/cursor/Dockerfile",
		"defs/opencode/Dockerfile",
		"defs/codex/Dockerfile",
		"defs/claudecode/mcp/Dockerfile",
	} {
		body := readRepoFile(t, rel)
		if !strings.Contains(body, "packages/lib/hooks/git-sync-turn.sh") {
			t.Errorf("%s must COPY git-sync-turn.sh so a leaf rebuild without a new base still ships it", rel)
		}
	}
}

func TestSeedRegistersTheGitSyncStopHookOnce(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	home := t.TempDir()
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"theme":"dark","hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo mine"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `source "$1/packages/lib/entrypoint-lib.sh"
agent_home="$2"
export HOME="$2" PROVEO_GIT_SYNC_HOOK="$3"
_proveo_agent_home() { printf '%s' "$agent_home"; }
proveo_install_git_sync_hooks claudecode
proveo_install_git_sync_hooks claudecode
echo DONE`
	out, err := exec.Command(bash, "-c", script, "bash", repoRoot(t), home, gitSyncScript(t)).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "DONE") {
		t.Fatalf("seed step failed: %v\n%s", err, out)
	}
	b, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var j struct {
		Theme string `json:"theme"`
		Hooks struct {
			Stop []struct {
				Hooks []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
					Timeout int    `json:"timeout"`
				} `json:"hooks"`
			} `json:"Stop"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatalf("settings.json is not JSON after the merge: %v\n%s", err, b)
	}
	if j.Theme != "dark" {
		t.Errorf("operator's theme was lost: %q", j.Theme)
	}
	syncs, mine := 0, 0
	for _, g := range j.Hooks.Stop {
		for _, h := range g.Hooks {
			switch {
			case strings.Contains(h.Command, "git-sync-turn.sh") && h.Type == "command":
				syncs++
				if h.Timeout == 0 {
					t.Error("the Stop hook needs a timeout: a hung push is worse than no hook")
				}
			case h.Command == "echo mine":
				mine++
			}
		}
	}
	if syncs != 1 {
		t.Errorf("git-sync registered %d times after two seeds, want exactly 1:\n%s", syncs, b)
	}
	if mine != 1 {
		t.Errorf("operator's own Stop hook did not survive the merge:\n%s", b)
	}
}

func TestSeedWritesOpenCodePluginAndNoCodexUserHook(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	home := t.TempDir()
	script := `source "$1/packages/lib/entrypoint-lib.sh"
agent_home="$2"
export HOME="$2" PROVEO_GIT_SYNC_HOOK="$3" PROVEO_GIT_SYNC_PLUGIN="$4"
_proveo_agent_home() { printf '%s' "$agent_home"; }
proveo_install_git_sync_hooks codex
proveo_install_git_sync_hooks opencode
echo DONE`
	out, err := exec.Command(bash, "-c", script, "bash", repoRoot(t), home, gitSyncScript(t), gitSyncPlugin(t)).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "DONE") {
		t.Fatalf("seed step failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.json")); err == nil {
		t.Error("seed wrote ~/.codex/hooks.json; codex holds user hooks for /hooks review, so the Stop hook must stay managed")
	}
	plugin := filepath.Join(home, ".config", "opencode", "plugins", "proveo-git-sync-turn.js")
	if _, err := os.Stat(plugin); err != nil {
		t.Errorf("opencode plugin was not seeded: %v", err)
	}
}

func TestCodexManagedRequirementsWireGitSyncStop(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "defs/codex/managed/requirements.toml")
	for _, want := range []string{"[[hooks.Stop]]", "[[hooks.Stop.hooks]]", "git-sync-turn.sh", "timeout = 90"} {
		if !strings.Contains(src, want) {
			t.Errorf("codex requirements.toml lacks %q", want)
		}
	}
	if df := readRepoFile(t, "defs/codex/Dockerfile"); !strings.Contains(df, "/etc/codex/requirements.toml") {
		t.Error("defs/codex/Dockerfile must install the managed requirements at /etc/codex/requirements.toml")
	}
}

func TestGitSyncCommitsOnlyToAReadOnlyOrigin(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	bare := t.TempDir()
	gitCmd(t, bare, nil, "init", "--bare", "-b", "main")
	gitCmd(t, dir, nil, "remote", "add", "origin", bare)
	gitCmd(t, dir, nil, "push", "-u", "origin", "HEAD")
	if err := os.Chmod(bare, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bare, 0o755) })
	if err := os.WriteFile(filepath.Join(dir, "clone.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errb, code := runGitSync(t, dir, `{"hook_event_name":"Stop"}`, hookEnv(t))
	if code != 0 || out != "" {
		t.Fatalf("read-only origin: exit %d stdout %q stderr %q, want exit 0 and no decision", code, out, errb)
	}
	if sub := gitCmd(t, dir, nil, "log", "-1", "--pretty=%s"); !strings.Contains(sub, "[proveo] persist turn") {
		t.Errorf("subject %q, want the persist commit", sub)
	}
	got := readGitSyncTrace(t, filepath.Join(dir, ".git", "proveo-git-sync.ndjson"))
	if len(got) != 1 || got[0].Result != "allow" || got[0].Error != "" {
		t.Errorf("read-only origin trace = %+v, want one allow with no error", got)
	}
}

func TestSeedCallsGitSyncHookInstall(t *testing.T) {
	t.Parallel()
	seed := seedBody(t, readRepoFile(t, "packages/lib/entrypoint-lib.sh"))
	if !strings.Contains(seed, `proveo_install_git_sync_hooks "$target"`) {
		t.Error("proveo_seed must call proveo_install_git_sync_hooks so both backends register the turn-end persist")
	}
	restore := strings.Index(seed, "proveo_sync_config restore")
	at := strings.Index(seed, "proveo_install_git_sync_hooks")
	if restore < 0 || at < restore {
		t.Error("git-sync install must run after config restore, or the restore overwrites the Stop merge")
	}
}

type gitSyncTraceLine struct {
	Dialect string `json:"dialect"`
	Result  string `json:"result"`
	Error   string `json:"error"`
}

func readGitSyncTrace(t *testing.T, path string) []gitSyncTraceLine {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trace %s: %v", path, err)
	}
	var out []gitSyncTraceLine
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r gitSyncTraceLine
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("trace line %q is not JSON: %v", l, err)
		}
		out = append(out, r)
	}
	return out
}

func TestGitSyncTraceRecordsEachOutcome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		dialect   string
		readOnly  bool
		want      gitSyncTraceLine
		wantError string
	}{
		{name: "pushed stop", dialect: "stop", want: gitSyncTraceLine{Dialect: "stop", Result: "allow"}},
		{name: "pushed idle", dialect: "idle", want: gitSyncTraceLine{Dialect: "idle", Result: "allow"}},
		{name: "push refused stop", dialect: "stop", readOnly: true,
			want: gitSyncTraceLine{Dialect: "stop", Result: "block"}, wantError: "git push failed"},
		{name: "push refused idle", dialect: "idle", readOnly: true,
			want: gitSyncTraceLine{Dialect: "idle", Result: "allow"}, wantError: "git push failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := initRepo(t)
			bare := t.TempDir()
			gitCmd(t, bare, nil, "init", "--bare", "-b", "main")
			gitCmd(t, dir, nil, "remote", "add", "origin", bare)
			gitCmd(t, dir, nil, "push", "-u", "origin", "HEAD")
			if tc.readOnly {
				objects := filepath.Join(bare, "objects")
				if err := os.Chmod(objects, 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(objects, 0o755) })
			}
			if err := os.WriteFile(filepath.Join(dir, "turn.txt"), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, errb, code := runGitSync(t, dir, `{"hook_event_name":"Stop"}`,
				hookEnv(t, "PROVEO_GIT_SYNC_DIALECT="+tc.dialect))
			if code != 0 {
				t.Fatalf("git-sync-turn exit %d, want 0; stderr %q", code, errb)
			}
			got := readGitSyncTrace(t, filepath.Join(dir, ".git", "proveo-git-sync.ndjson"))
			if len(got) != 1 {
				t.Fatalf("git-sync-turn(%s) wrote %d trace lines, want 1: %+v", tc.name, len(got), got)
			}
			if got[0].Dialect != tc.want.Dialect || got[0].Result != tc.want.Result {
				t.Errorf("git-sync-turn(%s) trace = %+v, want dialect=%s result=%s",
					tc.name, got[0], tc.want.Dialect, tc.want.Result)
			}
			if tc.wantError == "" && got[0].Error != "" {
				t.Errorf("git-sync-turn(%s) trace error = %q, want none", tc.name, got[0].Error)
			}
			if tc.wantError != "" && !strings.Contains(got[0].Error, tc.wantError) {
				t.Errorf("git-sync-turn(%s) trace error = %q, want it to name %q", tc.name, got[0].Error, tc.wantError)
			}
		})
	}
}

func TestGitSyncTraceHonoursItsOverride(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	alt := filepath.Join(t.TempDir(), "trace.ndjson")
	if _, _, code := runGitSync(t, dir, `{"hook_event_name":"Stop"}`, hookEnv(t, "PROVEO_GIT_SYNC_TRACE="+alt)); code != 0 {
		t.Fatalf("git-sync-turn exit %d, want 0", code)
	}
	if got := readGitSyncTrace(t, alt); len(got) != 1 || got[0].Result != "allow" {
		t.Errorf("PROVEO_GIT_SYNC_TRACE=%s trace = %+v, want one allow line", alt, got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "proveo-git-sync.ndjson")); err == nil {
		t.Error("an overridden trace also wrote the git-dir default")
	}

	off := initRepo(t)
	if _, _, code := runGitSync(t, off, `{"hook_event_name":"Stop"}`, hookEnv(t, "PROVEO_GIT_SYNC_TRACE=off")); code != 0 {
		t.Fatalf("git-sync-turn exit %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(off, ".git", "proveo-git-sync.ndjson")); err == nil {
		t.Error("PROVEO_GIT_SYNC_TRACE=off still wrote a trace")
	}
}

func TestOpenCodeLaunchWaitsForTheGitSyncPlugin(t *testing.T) {
	t.Parallel()
	df := readRepoFile(t, "defs/opencode/Dockerfile")
	for _, need := range []string{
		"packages/lib/proveo-await-seed /usr/local/bin/proveo-await-seed",
		"PROVEO_INSTRUCTIONS_MARKER=/dev/shm/proveo-hooks-seeded",
		"> /opt/proveo/shims/opencode",
		"/opt/proveo/shims:",
	} {
		if !strings.Contains(df, need) {
			t.Errorf("defs/opencode/Dockerfile lacks %q; the TUI loads plugins once at start and misses a later seed", need)
		}
	}
	seed := seedBody(t, readRepoFile(t, "packages/lib/entrypoint-lib.sh"))
	install := strings.Index(seed, `proveo_install_git_sync_hooks "$target"`)
	marker := strings.Index(seed, `: > "$PROVEO_HOOKS_MARKER"`)
	provision := strings.Index(seed, "proveo_provision_toolchain")
	if install < 0 || marker < install {
		t.Error("proveo_seed must mark the hooks seeded after installing them")
	}
	if provision >= 0 && marker > provision {
		t.Error("the hooks marker must land before toolchain provisioning, or the launch waits out its limit")
	}
}
