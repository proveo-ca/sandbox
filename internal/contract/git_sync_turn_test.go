// SPEC: _spec/packages/lib/git-sync-turn.puml
package contract_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

func hookEnv(t *testing.T, extra ...string) []string {
	t.Helper()
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + gitConfigHome(t),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=proveo-test",
		"GIT_AUTHOR_EMAIL=proveo@test",
		"GIT_COMMITTER_NAME=proveo-test",
		"GIT_COMMITTER_EMAIL=proveo@test",
		"PROVEO_GIT_SYNC_DIALECT=stop",
	}
	return append(env, extra...)
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
	if !strings.Contains(out, `"decision":"allow"`) {
		t.Errorf("stdout %q, want decision allow", out)
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
	if !strings.Contains(out, `"decision":"allow"`) {
		t.Errorf("stdout %q, want decision allow", out)
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
	if code != 0 || !strings.Contains(out, `"decision":"allow"`) {
		t.Errorf("non-repo: exit %d stdout %q", code, out)
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
		"PROVEO_GIT_SYNC_DIALECT=stop",
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
		"PROVEO_GIT_SYNC_DIALECT=stop",
	}
	if err := os.WriteFile(filepath.Join(home, "empty"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, code := runGitSync(t, dir, `{"hook_event_name":"Stop","stop_hook_active":true}`, env)
	if code != 0 {
		t.Fatalf("exit %d stdout %q", code, out)
	}
	if !strings.Contains(out, `"decision":"allow"`) {
		t.Errorf("stdout %q, want allow on a continuation so the 8-cap / loop_limit can rest", out)
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
		"PROVEO_GIT_SYNC_DIALECT=cursor",
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
	for _, banned := range []string{"--force", "--no-verify", "--amend", "git config "} {
		if strings.Contains(src, banned) {
			t.Errorf("git-sync-turn.sh contains %q", banned)
		}
	}
}

func TestGitSyncPluginListensForSessionIdle(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "packages/lib/hooks/proveo-git-sync-turn.js")
	for _, want := range []string{"session.idle", "git-sync-turn.sh", "PROVEO_GIT_SYNC_DIALECT=idle"} {
		if !strings.Contains(src, want) {
			t.Errorf("plugin lacks %q", want)
		}
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

func TestSeedWritesCodexStopAndOpenCodePlugin(t *testing.T) {
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
	hooks := filepath.Join(home, ".codex", "hooks.json")
	b, err := os.ReadFile(hooks)
	if err != nil {
		t.Fatalf("codex hooks.json missing: %v", err)
	}
	if !strings.Contains(string(b), "git-sync-turn.sh") || !strings.Contains(string(b), `"Stop"`) {
		t.Errorf("codex hooks.json missing Stop git-sync:\n%s", b)
	}
	plugin := filepath.Join(home, ".config", "opencode", "plugins", "proveo-git-sync-turn.js")
	if _, err := os.Stat(plugin); err != nil {
		t.Errorf("opencode plugin was not seeded: %v", err)
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
