// SPEC: _spec/internal/sbx/state-sync.puml
package contract_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/proveohome"
)

// stateBox is one sandbox's side of the state sync: its home, the agent's workdir and its /dev/shm.
type stateBox struct{ home, workdir, shm string }

func newStateBox(t *testing.T, workdir string) stateBox {
	t.Helper()
	return stateBox{home: t.TempDir(), workdir: workdir, shm: t.TempDir()}
}

func (b stateBox) projects(key string) string {
	return filepath.Join(b.home, ".claude", "projects", key)
}

func (b stateBox) released(t *testing.T) {
	t.Helper()
	seedFile(t, filepath.Join(b.shm, "released"), "")
}

func (b stateBox) restored() bool {
	_, err := os.Stat(filepath.Join(b.shm, "restored"))
	return err == nil
}

// syncState sources the lib with the claudecode manifest's state and config sets, box's home
// as plain directories, and runs one direction of the state sync against the host store state;
// each line of pre runs first, e.g. a background job.
func syncState(t *testing.T, bash string, box stateBox, state, mode string, pre ...string) (rc, out string) {
	t.Helper()
	return runSyncs(t, bash, box, state, "proveo_sync_state "+mode, pre...)
}

// runSyncs runs body in a shell that sourced the lib as the sandbox's teardown or seed does.
func runSyncs(t *testing.T, bash string, box stateBox, state, body string, pre ...string) (rc, out string) {
	t.Helper()
	home := claudeHome(t)
	script := `source "$1/packages/lib/entrypoint-lib.sh"
` + strings.Join(pre, "\n") + `
` + body + `
echo "rc=$?"`
	cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t))
	cmd.Env = append(os.Environ(), "HOME="+box.home, "PROVEO_HOME=", "PROVEO_STATE_HOME="+state,
		"PROVEO_CONFIG_SYNC=", "PROVEO_CONFIG_FILES=",
		proveohome.ConfigSetVar+"="+proveohome.ConfigSet(home),
		proveohome.StateSetVar+"="+proveohome.StateSet(home),
		"PROVEO_WORKDIR="+box.workdir,
		"PROVEO_STATE_RESTORED="+filepath.Join(box.shm, "restored"),
		"PROVEO_INSTRUCTIONS_MARKER="+filepath.Join(box.shm, "released"),
		"PROVEO_SEED_REFUSED="+filepath.Join(box.shm, "refused"),
		"PROVEO_SEED_HALTED="+filepath.Join(box.shm, "halted"))
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", body, err, b)
	}
	out = string(b)
	i := strings.LastIndex(out, "rc=")
	if i < 0 {
		t.Fatalf("%s printed no status:\n%s", body, out)
	}
	return strings.TrimSpace(out[i+3:]), out
}

// seedRestore and teardownSave run both syncs in the order the seed and SaveStateArgs do.
const (
	seedRestore  = "rc=0; proveo_sync_state restore || rc=1; proveo_sync_config restore || rc=1; (exit $rc)"
	teardownSave = "rc=0; proveo_sync_config save || rc=1; proveo_sync_state save || rc=1; (exit $rc)"
)

func mustSync(t *testing.T, bash string, box stateBox, state, mode string) {
	t.Helper()
	if rc, out := syncState(t, bash, box, state, mode); rc != "0" {
		t.Fatalf("proveo_sync_state %s rc=%s:\n%s", mode, rc, out)
	}
}

func readOr(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func backdate(t *testing.T, ago time.Duration, paths ...string) {
	t.Helper()
	at := time.Now().Add(-ago)
	for _, p := range paths {
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

func appendTo(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

// debris lists the sync's temp and staging names left under root.
func debris(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	_ = filepath.WalkDir(root, func(p string, _ os.DirEntry, _ error) error {
		if strings.Contains(filepath.Base(p), ".proveo-sync.") {
			found = append(found, p)
		}
		return nil
	})
	return found
}

// claudeKey is Claude Code's projects/ name for an ASCII path.
func claudeKey(p string) string {
	return strings.Map(func(r rune) rune {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, p)
}

// A kept sandbox reboots, or a stopped one boots for a teardown exec: its seed restores again,
// and a turn the volume holds that the host never got must outlive that restore.
func TestStateRestoreNeverRevertsANewerGuestTurn(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	state, box := t.TempDir(), newStateBox(t, "/work/ws-a")
	host := filepath.Join(state, ".claude", "projects", "-work-ws-a")

	seedFile(t, filepath.Join(host, "s.jsonl"), "a0\n")
	seedFile(t, filepath.Join(host, "host-newer.jsonl"), "host-v2\n")
	seedFile(t, filepath.Join(host, "same-stamp.jsonl"), "h\n")
	seedFile(t, filepath.Join(state, ".claude", "todos", "7.json"), `[{"status":"host"}]`)
	seedFile(t, box.projects("-work-ws-a")+"/s.jsonl", "a0\na2-unsaved\n")
	seedFile(t, box.projects("-work-ws-a")+"/host-newer.jsonl", "guest-v1\n")
	seedFile(t, box.projects("-work-ws-a")+"/same-stamp.jsonl", "gg\n")
	seedFile(t, filepath.Join(box.home, ".claude", "todos", "7.json"), `[{"status":"guest-newer"}]`)
	backdate(t, time.Hour, filepath.Join(host, "s.jsonl"), filepath.Join(state, ".claude", "todos", "7.json"))
	backdate(t, 2*time.Hour, box.projects("-work-ws-a")+"/host-newer.jsonl")
	stamp := time.Now().Add(-3 * time.Hour)
	for _, p := range []string{filepath.Join(host, "same-stamp.jsonl"), box.projects("-work-ws-a") + "/same-stamp.jsonl"} {
		if err := os.Chtimes(p, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	mustSync(t, bash, box, state, "restore")
	for _, c := range []struct{ rel, want, why string }{
		{"s.jsonl", "a0\na2-unsaved\n", "the host's older copy reverted the guest's unsaved turn"},
		{"host-newer.jsonl", "host-v2\n", "a copy the host holds newer did not reach the guest"},
		{"same-stamp.jsonl", "gg\n", "equal stamps are not proof the host is newer"},
	} {
		if got := readOr(filepath.Join(box.projects("-work-ws-a"), c.rel)); got != c.want {
			t.Errorf("after restore %s = %q, want %q: %s", c.rel, got, c.want, c.why)
		}
	}
	if got := readOr(filepath.Join(box.home, ".claude", "todos", "7.json")); !strings.Contains(got, "guest-newer") {
		t.Errorf("todos/ restore reverted the guest's newer entry: %q", got)
	}

	mustSync(t, bash, box, state, "save")
	if got := readOr(filepath.Join(host, "s.jsonl")); got != "a0\na2-unsaved\n" {
		t.Errorf("the next save did not carry the surviving turn home: %q", got)
	}
}

// Two live sandboxes in different workspaces share one host store; neither save may revert
// the other's turn, even from a stale copy an older restore left in a volume.
func TestTwoWorkspacesSharingOneStoreKeepEachOthersTurns(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	state := t.TempDir()
	a, b := newStateBox(t, "/work/ws-a"), newStateBox(t, "/work/ws-b")
	hostA := filepath.Join(state, ".claude", "projects", "-work-ws-a", "s.jsonl")
	hostB := filepath.Join(state, ".claude", "projects", "-work-ws-b", "s.jsonl")
	seedFile(t, hostA, "a0\n")
	seedFile(t, hostB, "b0\n")
	seedFile(t, b.projects("-work-ws-a")+"/s.jsonl", "a0\n")
	backdate(t, time.Hour, hostA, hostB, b.projects("-work-ws-a")+"/s.jsonl")

	mustSync(t, bash, a, state, "restore")
	mustSync(t, bash, b, state, "restore")
	if _, err := os.Stat(a.projects("-work-ws-b")); err == nil {
		t.Error("A's restore copied workspace B's projects/ entry")
	}
	appendTo(t, a.projects("-work-ws-a")+"/s.jsonl", "a1-turn-by-A\n")
	appendTo(t, b.projects("-work-ws-b")+"/s.jsonl", "b1-turn-by-B\n")

	mustSync(t, bash, a, state, "save")
	mustSync(t, bash, b, state, "save")
	if got := readOr(hostA); got != "a0\na1-turn-by-A\n" {
		t.Errorf("host wsA = %q after both saves; B's stale copy reverted A's turn", got)
	}
	if got := readOr(hostB); got != "b0\nb1-turn-by-B\n" {
		t.Errorf("host wsB = %q after both saves", got)
	}
}

// cpThatFillsTheVolume puts a cp on PATH that copies as cp does, then cuts every big.jsonl it
// created under vol short and fails, the way a full volume ends a copy.
func cpThatFillsTheVolume(t *testing.T, vol string) string {
	t.Helper()
	realCp, err := exec.LookPath("cp")
	if err != nil {
		t.Skipf("cp unavailable: %v", err)
	}
	bin := t.TempDir()
	shim := `#!/usr/bin/env bash
seen() { find '` + vol + `' -name 'big.jsonl*' -type f -printf '%i %p\n' 2>/dev/null; }
before="$(seen)"
'` + realCp + `' "$@"; rc=$?
cut=0
while IFS= read -r f; do
  grep -qxF -- "$f" <<< "$before" && continue
  truncate -s 4096 "${f#* }" && cut=1
done < <(seen)
if ((cut)); then
  echo "cp: error writing: No space left on device" >&2
  exit 1
fi
exit $rc
`
	if err := os.WriteFile(filepath.Join(bin, "cp"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// A restore into a volume that fills mid-file: the cut copy never takes a real name, this boot's
// save refuses, and the host file stays whole through the next boot.
func TestARestoreThatFailsMidFileNeverTruncatesTheHost(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	state, box := t.TempDir(), newStateBox(t, "/work/ws")
	host := filepath.Join(state, ".claude", "projects", "-work-ws")
	big := bytes.Repeat([]byte(`{"type":"assistant","text":"0123456789abcdef"}`+"\n"), 8000)
	seedFile(t, filepath.Join(host, "big.jsonl"), string(big))
	seedFile(t, filepath.Join(host, "small.jsonl"), "small\n")
	seedFile(t, filepath.Join(host, "kept", "big.jsonl"), string(big))
	seedFile(t, box.projects("-work-ws")+"/kept/big.jsonl", "older but whole\n")
	backdate(t, time.Hour, filepath.Join(host, "big.jsonl"), filepath.Join(host, "small.jsonl"), filepath.Join(host, "kept", "big.jsonl"))
	backdate(t, 2*time.Hour, box.projects("-work-ws")+"/kept/big.jsonl")
	shim := cpThatFillsTheVolume(t, box.home)

	rc, out := syncState(t, bash, box, state, "restore", `export PATH="`+shim+`:$PATH"`)
	if rc == "0" || !strings.Contains(out, "state restore completed with copy errors") {
		t.Fatalf("a restore that ran out of space reported rc=%s:\n%s", rc, out)
	}
	if _, err := os.Stat(box.projects("-work-ws") + "/big.jsonl"); err == nil {
		t.Error("the cut copy of big.jsonl took its real name in the volume")
	}
	if got := readOr(box.projects("-work-ws") + "/kept/big.jsonl"); got != "older but whole\n" {
		t.Errorf("a failed replace damaged the volume's own copy: %d bytes", len(got))
	}
	if got := readOr(box.projects("-work-ws") + "/small.jsonl"); got != "small\n" {
		t.Errorf("the files that fit did not land: %q", got)
	}
	if d := debris(t, box.home); len(d) > 0 {
		t.Errorf("the failed restore left debris: %v", d)
	}
	if box.restored() {
		t.Error("a restore with copy errors wrote this boot's restore marker")
	}

	seedFile(t, box.projects("-work-ws")+"/new.jsonl", "a turn this boot wrote\n")
	box.released(t)
	rc, out = syncState(t, bash, box, state, "save")
	if rc == "0" || !strings.Contains(out, "state save refused") {
		t.Errorf("the save after a failed restore ran (rc=%s):\n%s", rc, out)
	}
	if got := readOr(filepath.Join(host, "big.jsonl")); got != string(big) {
		t.Errorf("host big.jsonl is %d bytes after the save, want %d", len(got), len(big))
	}

	mustSync(t, bash, box, state, "restore")
	if !box.restored() {
		t.Error("a clean restore wrote no marker")
	}
	mustSync(t, bash, box, state, "save")
	for _, p := range []string{filepath.Join(host, "big.jsonl"), box.projects("-work-ws") + "/big.jsonl"} {
		if got := readOr(p); got != string(big) {
			t.Errorf("%s is %d bytes after the next boot, want %d", p, len(got), len(big))
		}
	}
	if got := readOr(filepath.Join(host, "new.jsonl")); got != "a turn this boot wrote\n" {
		t.Errorf("the next boot's save did not carry the waiting turn: %q", got)
	}
}

// A directory whose new names overflow one exec's argument list still restores whole; the
// 512 KiB stack drops that limit to its 128 KiB floor.
func TestARestorePastTheArgumentLimitPlacesEveryFile(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	state, box := t.TempDir(), newStateBox(t, "/work/ws")
	host := filepath.Join(state, ".claude", "projects", "-work-ws")
	const files = 4000
	for i := range files {
		seedFile(t, filepath.Join(host, fmt.Sprintf("session-%060d.jsonl", i)), "turn\n")
	}

	rc, out := syncState(t, bash, box, state, "restore", "ulimit -s 512")
	entries, _ := os.ReadDir(box.projects("-work-ws"))
	if rc != "0" || len(entries) != files || !box.restored() {
		t.Errorf("restore rc=%s placed %d of %d files, marker %v:\n%s", rc, len(entries), files, box.restored(), out)
	}
	if d := debris(t, box.home); len(d) > 0 {
		t.Errorf("the restore left %d debris entries, e.g. %s", len(d), d[0])
	}
}

func TestStateSaveRefusesWithoutThisBootsRestore(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	state, box := t.TempDir(), newStateBox(t, "/work/ws")
	host := filepath.Join(state, ".claude", "projects", "-work-ws", "s.jsonl")
	seedFile(t, host, "host\n")
	backdate(t, time.Hour, host)
	seedFile(t, box.projects("-work-ws")+"/s.jsonl", "guest\n")

	box.released(t)
	rc, out := syncState(t, bash, box, state, "save")
	if rc == "0" || !strings.Contains(out, "state save refused") {
		t.Errorf("a save with no restore this boot gave rc=%s:\n%s", rc, out)
	}
	if got := readOr(host); got != "host\n" {
		t.Errorf("the refused save still wrote the host: %q", got)
	}

	if err := os.Remove(filepath.Join(box.shm, "released")); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(box.shm, "restored")
	rc, out = syncState(t, bash, box, state, "save", `( sleep 1; : > '`+restored+`' ) &`)
	if rc != "0" {
		t.Errorf("a save that started while the seed was restoring did not wait for it (rc=%s):\n%s", rc, out)
	}
	if got := readOr(host); got != "guest\n" {
		t.Errorf("the save after the restore finished left the host at %q", got)
	}

	empty, cold := t.TempDir(), newStateBox(t, "/work/ws")
	start := time.Now()
	if rc, out := syncState(t, bash, cold, empty, "save"); rc != "0" || time.Since(start) > 10*time.Second {
		t.Errorf("a sandbox with nothing to save gave rc=%s after %s:\n%s", rc, time.Since(start), out)
	}
}

// gitAt runs git in dir with a throwaway identity and no user config.
func gitAt(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// Restore brings back this run's projects/ entries only: the transcripts of every worktree of
// the workdir's repository, which `claude --resume` lists, and the repository root's memory.
// todos/ keeps its whole-store scope; sessions/ stays in the VM that wrote it.
func TestStateRestoreCopiesOnlyThisWorkspacesProjects(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	repos := t.TempDir()
	main, wt, sibling := filepath.Join(repos, "main"), filepath.Join(repos, "wt"), filepath.Join(repos, "sibling")
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, main, "init", "-q", "-b", "main")
	gitAt(t, main, "commit", "-q", "--allow-empty", "-m", "base")
	gitAt(t, main, "worktree", "add", "-q", "-b", "agent", wt)
	gitAt(t, main, "worktree", "add", "-q", "-b", "other", sibling)

	state, box := t.TempDir(), newStateBox(t, wt)
	store := filepath.Join(state, ".claude", "projects")
	seedFile(t, filepath.Join(store, claudeKey(wt), "t.jsonl"), "transcript\n")
	seedFile(t, filepath.Join(store, claudeKey(sibling), "s.jsonl"), "a sibling worktree's session\n")
	seedFile(t, filepath.Join(store, claudeKey(main), "memory", "MEMORY.md"), "memory\n")
	seedFile(t, filepath.Join(store, "-work-other", "x.jsonl"), "another workspace\n")
	seedFile(t, filepath.Join(state, ".claude", "sessions", "1.json"), "{}")
	seedFile(t, filepath.Join(state, ".claude", "todos", "1.json"), "[]")

	mustSync(t, bash, box, state, "restore")
	entries, _ := os.ReadDir(filepath.Join(box.home, ".claude", "projects"))
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	want := []string{claudeKey(main), claudeKey(sibling), claudeKey(wt)}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("restored projects/ entries = %v, want %v", got, want)
	}
	if readOr(box.projects(claudeKey(main))+"/memory/MEMORY.md") != "memory\n" {
		t.Error("the repository root's memory did not reach the worktree's run")
	}
	if _, err := os.Stat(filepath.Join(box.home, ".claude", "todos", "1.json")); err != nil {
		t.Errorf("todos/1.json left its whole-store scope: %v", err)
	}
	if _, err := os.Stat(filepath.Join(box.home, ".claude", "sessions")); err == nil {
		t.Error("the state restore carried sessions/ into the VM; it is VM-local")
	}
}

// The encoding Claude Code 2.1.323 applies (cli: replace(/[^a-zA-Z0-9]/g,"-"), then over 200
// units a slice plus a hash); the ASCII rows have the shapes of names a host store holds.
func TestClaudeProjectKeyMatchesClaudeCode(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	for _, c := range []struct{ path, want string }{
		{"/Users/dev/Projects/app", "-Users-dev-Projects-app"},
		{"/Users/dev/Projects/app/.claude/worktrees/feat-x", "-Users-dev-Projects-app--claude-worktrees-feat-x"},
		{"/private/var/folders/z0/T/TestHelloWorldE2Eclaudecode1062873134-001",
			"-private-var-folders-z0-T-TestHelloWorldE2Eclaudecode1062873134-001"},
		{"/work/my repo_v2.1", "-work-my-repo-v2-1"},
		{"/x/é", "-x--"},
		{"/x/" + string(rune(0x1F600)), "-x---"},
	} {
		script := `source "$1/packages/lib/entrypoint-lib.sh"; _proveo_claude_project_key "$2"`
		out, err := exec.Command(bash, "-c", script, "bash", repoRoot(t), c.path).CombinedOutput()
		if err != nil || string(out) != c.want {
			t.Errorf("key(%q) = %q (%v), want %q", c.path, out, err, c.want)
		}
	}

	long := "/w/" + strings.Repeat("a", 250)
	prefix := claudeKey(long)[:200]
	store := t.TempDir()
	for _, d := range []string{prefix + "-1k2j3h", claudeKey("/w/" + strings.Repeat("b", 250))[:200] + "-zz9"} {
		if err := os.MkdirAll(filepath.Join(store, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := `source "$1/packages/lib/entrypoint-lib.sh"; _proveo_claude_project_dirs "$2"`
	cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t), store)
	cmd.Env = append(os.Environ(), "PROVEO_WORKDIR="+long)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != prefix+"-1k2j3h" {
		t.Errorf("a workdir over 200 units matched %q (%v), want only its hashed entry", out, err)
	}
}

// Save waits for this boot's restore by the seed's release; that holds only while the
// release follows the restore.
func TestSeedRestoresStateBeforeItReleasesTheAgent(t *testing.T) {
	t.Parallel()
	seed := seedBody(t, entrypointLib(t))
	restore := strings.Index(seed, "proveo_sync_state restore")
	release := strings.LastIndex(seed, "proveo_release_agent")
	if restore < 0 || release < restore {
		t.Errorf("proveo_seed releases the agent (at %d) before its state restore (at %d)", release, restore)
	}
}

// With no volume@1 the state dirs are plain directories of the image home; the manifest's list,
// not /proc/mounts, tells the sync which ones to carry.
func TestProjectsAndTodosRoundTripOnPlainDirectories(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	state, box := t.TempDir(), newStateBox(t, "/work/ws")
	host := filepath.Join(state, ".claude")
	seedFile(t, filepath.Join(host, "projects", "-work-ws", "s.jsonl"), "earlier\n")
	seedFile(t, filepath.Join(host, "todos", "s-agent.json"), `[{"content":"earlier"}]`)
	backdate(t, time.Hour, filepath.Join(host, "projects", "-work-ws", "s.jsonl"), filepath.Join(host, "todos", "s-agent.json"))

	if rc, out := runSyncs(t, bash, box, state, seedRestore); rc != "0" || !box.restored() {
		t.Fatalf("restore rc=%s, marker %v:\n%s", rc, box.restored(), out)
	}
	for _, rel := range []string{"projects/-work-ws/s.jsonl", "todos/s-agent.json"} {
		if _, err := os.Stat(filepath.Join(box.home, ".claude", rel)); err != nil {
			t.Errorf("restore did not bring .claude/%s into the VM: %v", rel, err)
		}
	}
	appendTo(t, box.projects("-work-ws")+"/s.jsonl", "this run\n")
	seedFile(t, box.projects("-work-ws")+"/new.jsonl", "a new session\n")
	seedFile(t, filepath.Join(box.home, ".claude", "todos", "new-agent.json"), "[]")

	if rc, out := runSyncs(t, bash, box, state, teardownSave); rc != "0" {
		t.Fatalf("save rc=%s:\n%s", rc, out)
	}
	for rel, want := range map[string]string{
		"projects/-work-ws/s.jsonl":   "earlier\nthis run\n",
		"projects/-work-ws/new.jsonl": "a new session\n",
		"todos/new-agent.json":        "[]",
	} {
		if got := readOr(filepath.Join(host, rel)); got != want {
			t.Errorf("host .claude/%s = %q after the save, want %q", rel, got, want)
		}
	}
}

// Claude's VM-local paths never leave their VM and never come back, through either sync, and the
// host's existing copies stay as they are.
func TestVMLocalPathsNeverCrossEitherSync(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	state, box := t.TempDir(), newStateBox(t, "/work/ws")
	host := filepath.Join(state, ".claude")
	hostOwn := []string{"sessions/1200.json", "statsig/cache", "shell-snapshots/snap.sh", "daemon.lock",
		"daemon.status.json", "daemon.log", "daemon/roster.json", "jobs/j1/state.json", "jobs/j1/tmp/main.go"}
	guestOnly := []string{"sessions/2.json", "sessions/2.abc.key", "statsig/new", "shell-snapshots/new.sh",
		"daemon/control.key", "jobs/j2/state.json"}
	for _, rel := range hostOwn {
		seedFile(t, filepath.Join(host, rel), "host")
		backdate(t, time.Hour, filepath.Join(host, rel))
	}
	seedFile(t, filepath.Join(host, "settings.json"), "{}")

	if rc, out := runSyncs(t, bash, box, state, seedRestore); rc != "0" {
		t.Fatalf("restore rc=%s:\n%s", rc, out)
	}
	if _, err := os.Stat(filepath.Join(box.home, ".claude", "settings.json")); err != nil {
		t.Fatalf("the config restore carried nothing, so the skips prove nothing: %v", err)
	}
	for _, rel := range hostOwn {
		if _, err := os.Lstat(filepath.Join(box.home, ".claude", rel)); err == nil {
			t.Errorf("restore carried .claude/%s into the VM", rel)
		}
	}

	for _, rel := range slices.Concat(hostOwn, guestOnly) {
		seedFile(t, filepath.Join(box.home, ".claude", rel), "guest")
	}
	box.released(t)
	if rc, out := runSyncs(t, bash, box, state, teardownSave); rc != "0" {
		t.Fatalf("save rc=%s:\n%s", rc, out)
	}
	for _, rel := range hostOwn {
		if got := readOr(filepath.Join(host, rel)); got != "host" {
			t.Errorf("host .claude/%s = %q after the save, want the host's own copy untouched", rel, got)
		}
	}
	for _, rel := range guestOnly {
		if _, err := os.Lstat(filepath.Join(host, rel)); err == nil {
			t.Errorf("save carried the VM's .claude/%s to the host", rel)
		}
	}
}

// A config save leaves projects/ to the state save.
func TestAConfigSaveNoLongerCarriesProjects(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	state, box := t.TempDir(), newStateBox(t, "/work/ws")
	seedFile(t, box.projects("-work-ws")+"/s.jsonl", "turn\n")
	seedFile(t, filepath.Join(box.home, ".claude", "settings.json"), "{}")

	if rc, out := runSyncs(t, bash, box, state, "proveo_sync_config save"); rc != "0" {
		t.Fatalf("config save rc=%s:\n%s", rc, out)
	}
	if _, err := os.Stat(filepath.Join(state, ".claude", "settings.json")); err != nil {
		t.Errorf("config save dropped settings.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, ".claude", "projects")); err == nil {
		t.Error("config save carried .claude/projects, which the state sync owns")
	}

	box.released(t)
	seedFile(t, filepath.Join(box.shm, "restored"), "")
	mustSync(t, bash, box, state, "save")
	if got := readOr(filepath.Join(state, ".claude", "projects", "-work-ws", "s.jsonl")); got != "turn\n" {
		t.Errorf("state save left host projects/-work-ws/s.jsonl at %q", got)
	}
}

// olderCLI makes the shell a sandbox that a CLI predating PROVEO_STATE_DIRS created on this image:
// its env carries the old transient list, and its kit mounts the five volume@1 of sbx/claude 2.1.267.
const olderCLI = `unset ` + proveohome.StateSetVar + `; export ` + proveohome.ConfigSetVar + `='.claude|.claude|jobs/*/tmp'
_proveo_volume_dirs() { printf '%s\n' .claude/projects .claude/sessions .claude/todos .claude/shell-snapshots .claude/statsig; }`

// Without PROVEO_STATE_DIRS the kit's volumes are the state dirs: projects/ and todos/ travel by
// the state sync, the other volumes stay in the VM, and a restore that fills a volume mid-file
// leaves the host whole.
func TestWithoutStateDirsTheKitVolumesTravelAsState(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	state, box := t.TempDir(), newStateBox(t, "/work/ws")
	host := filepath.Join(state, ".claude")
	hostBig := filepath.Join(host, "projects", "-work-ws", "big.jsonl")
	big := strings.Repeat(`{"type":"assistant","text":"0123456789abcdef"}`+"\n", 8000)
	seedFile(t, hostBig, big)
	seedFile(t, filepath.Join(host, "projects", "-work-other", "x.jsonl"), "another workspace\n")
	seedFile(t, filepath.Join(host, "todos", "t.json"), "[]")
	seedFile(t, filepath.Join(host, "sessions", "1200.json"), "host")
	seedFile(t, filepath.Join(host, "settings.json"), "{}")
	backdate(t, time.Hour, hostBig, filepath.Join(host, "sessions", "1200.json"))
	shim := cpThatFillsTheVolume(t, box.home)

	rc, out := runSyncs(t, bash, box, state, seedRestore, olderCLI, `export PATH="`+shim+`:$PATH"`)
	if rc == "0" || box.restored() {
		t.Errorf("a restore that ran out of space gave rc=%s, marker %v:\n%s", rc, box.restored(), out)
	}
	for _, rel := range []string{"projects/-work-ws/big.jsonl", "projects/-work-other", "sessions/1200.json"} {
		if _, err := os.Lstat(filepath.Join(box.home, ".claude", rel)); err == nil {
			t.Errorf("the restore placed .claude/%s in the VM", rel)
		}
	}
	for _, rel := range []string{"settings.json", "todos/t.json"} {
		if _, err := os.Stat(filepath.Join(box.home, ".claude", rel)); err != nil {
			t.Errorf("the restore did not bring .claude/%s into the VM: %v", rel, err)
		}
	}

	seedFile(t, filepath.Join(box.home, ".claude", "sessions", "2.json"), "guest")
	seedFile(t, filepath.Join(box.home, ".claude", "statsig", "s"), "guest")
	box.released(t)
	rc, out = runSyncs(t, bash, box, state, teardownSave, olderCLI)
	if rc == "0" || !strings.Contains(out, "state save refused") {
		t.Errorf("the save after a failed restore gave rc=%s:\n%s", rc, out)
	}
	if got := readOr(hostBig); got != big {
		t.Errorf("host big.jsonl is %d bytes after the save, want %d", len(got), len(big))
	}

	if rc, out := runSyncs(t, bash, box, state, seedRestore, olderCLI); rc != "0" || !box.restored() {
		t.Fatalf("the next boot's restore gave rc=%s, marker %v:\n%s", rc, box.restored(), out)
	}
	appendTo(t, box.projects("-work-ws")+"/big.jsonl", "this run\n")
	if rc, out := runSyncs(t, bash, box, state, teardownSave, olderCLI); rc != "0" {
		t.Fatalf("the next boot's save gave rc=%s:\n%s", rc, out)
	}
	if got := readOr(hostBig); got != big+"this run\n" {
		t.Errorf("host big.jsonl is %d bytes after the next save, want %d", len(got), len(big)+len("this run\n"))
	}
	if got := readOr(filepath.Join(host, "sessions", "1200.json")); got != "host" {
		t.Errorf("host .claude/sessions/1200.json = %q after the save, want the host's own copy", got)
	}
	for _, rel := range []string{"sessions/2.json", "statsig/s"} {
		if _, err := os.Lstat(filepath.Join(host, rel)); err == nil {
			t.Errorf("the save carried the VM's .claude/%s to the host", rel)
		}
	}
}
