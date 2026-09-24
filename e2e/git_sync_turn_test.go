//go:build e2e

// SPEC: _spec/tests/44-git-sync-turn-e2e.puml, _spec/packages/lib/git-sync-turn.puml

package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/tmux"
)

const hookProbeFile = "HOOK_PROBE.txt"

// gitSyncHarness is one def that wires git-sync-turn at turn end.
type gitSyncHarness struct {
	target     string
	localModel bool
	dialect    string
	ready      string
}

var gitSyncHarnesses = []gitSyncHarness{
	{target: "claudecode", localModel: true, dialect: "stop", ready: "bypass permissions"},
	{target: "codex", localModel: true, dialect: "stop", ready: "Ask Codex to do anything"},
	{target: "cursor", dialect: "cursor", ready: "Cursor Agent"},
	{target: "opencode", localModel: true, dialect: "idle", ready: "Ask anything"},
}

// gitSyncDialogs are first-run screens that stand between launch and the prompt.
var gitSyncDialogs = []string{"Trust this folder?", "Do you trust the files in this folder?"}

// gitSyncHookRejected is how a harness reports a hook it ran but refused.
var gitSyncHookRejected = regexp.MustCompile(`(?i)hook (error|failed)|invalid (hook|json)[^\n]*hook|hook[^\n]*(invalid|not trusted|untrusted|needs review)`)

const gitSyncPrompt = "Reply with the single word OK and nothing else."

// TestGitSyncTurnHookFiresBeforeThePromptReturns drives each harness TUI through
// one turn, then reads the hook's trace and the clone's git state in the live sandbox.
func TestGitSyncTurnHookFiresBeforeThePromptReturns(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	proveoBin := buildProveo(t)
	probe, err := os.ReadFile(filepath.Join("testdata", "git-sync-turn", hookProbeFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range gitSyncHarnesses {
		t.Run(h.target, func(t *testing.T) {
			requireHarness(t, h.target)
			runGitSyncTurn(t, h, proveoBin, probe)
		})
	}
}

func runGitSyncTurn(t *testing.T, h gitSyncHarness, proveoBin string, probe []byte) {
	t.Helper()
	var envArgs, runArgs []string
	if h.localModel {
		envArgs = childEnvArgsNoCredential(t)
		runArgs = []string{"--local-model", localModel(t)}
	} else {
		requireHarnessCredential(t, h.target)
		envArgs = childEnvArgsFor(t, harnessSecrets(t, h.target)[0])
	}
	work := gitSyncWorkspace(t)
	before, canList := sbxSandboxNames()

	sess := tmux.New(fmt.Sprintf("proveo-gitsync-%s-%d", h.target, os.Getpid()), nil)
	t.Cleanup(func() {
		sess.Kill()
		removeLeakedSandboxes(t, before, canList)
	})
	cmd := append([]string{"env"}, envArgs...)
	cmd = append(cmd, "PROVEO_HOME="+t.TempDir(), "PROVEO_AUTO_INSTALL_TOOLS=false",
		proveoBin, "run", h.target, "--egress-mode", "open", "--credentials", "forward", "--input", work)
	if err := sess.Start(220, 50, append(cmd, runArgs...)...); err != nil {
		t.Fatalf("start %s: %v", h.target, err)
	}
	w := newWatcher(t, sess)
	launched := func() string {
		s := w.Screen()
		if i := strings.LastIndex(s, "Workspace: "); i >= 0 {
			return s[i:]
		}
		return ""
	}

	startup := durationEnv(t, "PROVEO_TEST_TIMEOUT", 8*time.Minute)
	w.until("the "+h.target+" prompt", startup, func() bool {
		tui := launched()
		for _, d := range gitSyncDialogs {
			if strings.Contains(tui, d) && !strings.Contains(tui, h.ready) {
				_ = sess.Enter()
				time.Sleep(2 * time.Second)
				return false
			}
		}
		return strings.Contains(tui, h.ready)
	})
	var name string
	if m := sandboxNameRE.FindStringSubmatch(w.Screen()); m != nil {
		name = m[1]
	} else if fresh := newSandboxes(before); len(fresh) == 1 {
		name = fresh[0]
	} else {
		w.Fatalf("cannot name the sandbox %s runs in (new: %v)", h.target, fresh)
	}
	time.Sleep(3 * time.Second)

	drop := gitSyncCd(work) + "\nprintf %s " + shellQuote([]string{base64.StdEncoding.EncodeToString(probe)}) +
		" | base64 -d > " + hookProbeFile + "\nchown --reference=. " + hookProbeFile + " 2>/dev/null || true\n"
	if out, err := exec.Command(sbx.Binary, "exec", "-w", "/", name, "--", "bash", "-c", drop).CombinedOutput(); err != nil {
		t.Fatalf("drop %s into %s: %v\n%s", hookProbeFile, name, err, out)
	}
	mark := len(w.Screen())
	if err := sess.SendText(gitSyncPrompt); err != nil {
		t.Fatalf("type the prompt: %v", err)
	}
	time.Sleep(time.Second)
	if err := sess.Enter(); err != nil {
		t.Fatalf("submit the prompt: %v", err)
	}

	var state map[string]string
	w.until("the git-sync-turn trace — the hook never ran at turn end", durationEnv(t, "PROVEO_TEST_TURN_TIMEOUT", 8*time.Minute), func() bool {
		state = gitSyncState(t, name, work)
		return state["TRACE"] != ""
	})
	time.Sleep(5 * time.Second)
	w.tick()
	state = gitSyncState(t, name, work)
	after := w.Screen()
	if mark > len(after) {
		mark = 0
	}
	if m := gitSyncHookRejected.FindString(plain(after[mark:])); m != "" {
		t.Errorf("%s reported the hook as failed (%q) although the script ran\n%s", h.target, m, lastLines(after[mark:], 30))
	}
	assertGitSyncTurn(t, h, state, probe)
}

// gitSyncWorkspace copies the fixture's committed base into a fresh repo.
func gitSyncWorkspace(t *testing.T) string {
	t.Helper()
	work := t.TempDir()
	src := filepath.Join("testdata", "git-sync-turn", "workspace")
	if out, err := exec.Command("cp", "-a", src+"/.", work).CombinedOutput(); err != nil {
		t.Fatalf("copy git-sync-turn fixture: %v\n%s", err, out)
	}
	gitInit(t, work)
	return work
}

func gitSyncCd(work string) string {
	dirs := []string{work}
	if real, err := filepath.EvalSymlinks(work); err == nil && real != work {
		dirs = append(dirs, real)
	}
	return "for d in " + shellQuote(dirs) + `; do [ -e "$d/.git" ] && cd "$d" && break; done
git rev-parse --show-toplevel >/dev/null 2>&1 || { echo "no repository at ` + work + `"; exit 3; }`
}

// gitSyncState reads the hook trace and the probe file's git state from inside the sandbox.
func gitSyncState(t *testing.T, name, work string) map[string]string {
	t.Helper()
	script := "export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0='*'\n" +
		gitSyncCd(work) + `
gd=$(git rev-parse --git-dir)
printf 'TRACE=%s\n' "$(base64 < "$gd/proveo-git-sync.ndjson" 2>/dev/null | tr -d '\n')"
printf 'SUBJECT=%s\n' "$(git log -1 --format=%s -- ` + hookProbeFile + `)"
printf 'DIRTY=%s\n' "$(git status --porcelain -- ` + hookProbeFile + ` | tr '\n' ' ')"
printf 'BLOB=%s\n' "$(git show HEAD:` + hookProbeFile + ` 2>/dev/null | base64 | tr -d '\n')"
printf 'REMOTES=%s\n' "$(git remote | tr '\n' ' ')"
printf 'UPSTREAM=%s\n' "$(git rev-parse --abbrev-ref '@{u}' 2>/dev/null)"
printf 'AHEAD=%s\n' "$(git rev-list --count '@{u}..HEAD' 2>/dev/null)"
u=$(git remote get-url origin 2>/dev/null); [ -d "$u" ] && [ ! -w "$u" ] && echo ORIGIN_RO=1
`
	out, err := exec.Command(sbx.Binary, "exec", "-w", "/", name, "--", "bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Logf("sbx exec %s: %v\n%s", name, err, out)
		return nil
	}
	state := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimRight(l, "\r"), "="); ok {
			state[k] = strings.TrimSpace(v)
		}
	}
	return state
}

type gitSyncTrace struct {
	Dialect string `json:"dialect"`
	Event   string `json:"event"`
	Result  string `json:"result"`
	Error   string `json:"error"`
}

func assertGitSyncTurn(t *testing.T, h gitSyncHarness, state map[string]string, probe []byte) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(state["TRACE"])
	if err != nil {
		t.Fatalf("decode trace %q: %v", state["TRACE"], err)
	}
	var traces []gitSyncTrace
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r gitSyncTrace
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Errorf("trace line %q is not JSON: %v", l, err)
			continue
		}
		traces = append(traces, r)
	}
	for i, r := range traces {
		if r.Dialect != h.dialect {
			t.Errorf("%s trace[%d].dialect = %q, want %q", h.target, i, r.Dialect, h.dialect)
		}
		if r.Result != "allow" || r.Error != "" {
			t.Errorf("%s trace[%d] = result %q error %q, want allow with no error", h.target, i, r.Result, r.Error)
		}
	}
	if got := state["SUBJECT"]; got != "[proveo] persist turn" {
		t.Errorf("%s: last commit touching %s = %q, want %q", h.target, hookProbeFile, got, "[proveo] persist turn")
	}
	if got := state["DIRTY"]; got != "" {
		t.Errorf("%s: %s still dirty after the hook: %q", h.target, hookProbeFile, got)
	}
	if blob, _ := base64.StdEncoding.DecodeString(state["BLOB"]); !bytes.Equal(blob, probe) {
		t.Errorf("%s: HEAD:%s = %q, want the fixture %q", h.target, hookProbeFile, blob, probe)
	}
	if state["REMOTES"] != "" && state["ORIGIN_RO"] == "" {
		if state["UPSTREAM"] == "" {
			t.Errorf("%s: remotes %q but HEAD has no upstream — the hook's push never landed", h.target, state["REMOTES"])
		} else if state["AHEAD"] != "0" {
			t.Errorf("%s: HEAD is %s commit(s) ahead of %s — the hook left work unpushed", h.target, state["AHEAD"], state["UPSTREAM"])
		}
	}
	if !t.Failed() {
		delivery := "committed and pushed"
		if state["ORIGIN_RO"] != "" {
			delivery = "committed; origin is read-only, so teardown carries it"
		}
		t.Logf("%s: %d hook run(s), all allow; %s %s", h.target, len(traces), hookProbeFile, delivery)
	}
}
