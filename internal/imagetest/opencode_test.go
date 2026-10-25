//go:build image

// SPEC: _spec/tests/testing-strategy.puml
package imagetest_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/imagetest"
	"github.com/proveo-ca/proveo/internal/sbx"
)

var ocSeq atomic.Int64

// ocRun runs `docker run --name <unique> args...` bounded by timeout and
// force-removes the container afterwards, so a timed-out client leaves nothing behind.
func ocRun(t *testing.T, timeout time.Duration, env []string, args ...string) imagetest.Result {
	t.Helper()
	return ocRunStdin(t, timeout, env, "", args...)
}

// ocRunStdin is ocRun with stdin attached (`docker run -i`) when stdin is not empty.
func ocRunStdin(t *testing.T, timeout time.Duration, env []string, stdin string, args ...string) imagetest.Result {
	t.Helper()
	name := fmt.Sprintf("proveo-imagetest-opencode-%d-%d", os.Getpid(), ocSeq.Add(1))
	t.Cleanup(func() { imagetest.Docker(30*time.Second, nil, "rm", "-f", name) })
	run := []string{"run", "--rm", "--name", name}
	if stdin != "" {
		run = append(run, "-i")
	}
	return imagetest.DockerStdin(timeout, env, stdin, append(run, args...)...)
}

// ocNoInstalls keeps the config checks off the network: a markdown fixture made
// the seed download marksman, and that alone outran the 30s budget.
var ocNoInstalls = []string{"-e", "PROVEO_LSP_INSTALL=off", "-e", "PROVEO_AUTO_INSTALL_TOOLS=false"}

// ocFixtureProvider is an unreachable OpenAI-compatible provider: a run creates its session, then retries until killed.
const ocFixtureProvider = `{"plugins":[],"providers":{"fixture":{"package":"@opencode/ai/providers/openai-compatible",` +
	`"settings":{"baseURL":"http://127.0.0.1:9/v1","apiKey":"synthetic"},` +
	`"models":{"model":{"name":"Offline fixture","limit":{"context":100000,"output":4096}}}}}}`

// ocEngineHistory launches twice through the shim and finds the first launch's session in the native default database.
// SPEC: _spec/defs/opencode/engine-local-history.puml
const ocEngineHistory = `set -u
shim=/opt/proveo/shims/opencode
db="$HOME/.local/share/opencode/opencode.db"
mkdir -p /tmp/work && cd /tmp/work || exit 40
[ ! -e "$db" ] || exit 41
timeout 15 "$shim" run --model fixture/model "offline history fixture" >/tmp/run.log 2>&1
[ -f "$db" ] || { cat /tmp/run.log; exit 42; }
out=$("$shim" session list --format json 2>/tmp/list.err) || { cat /tmp/list.err; exit 43; }
printf '%s\n' "$out" | grep -q '"directory": *"/tmp/work"' || { echo "$out"; cat /tmp/run.log; exit 44; }
[ "$(find /tmp "$HOME" -name opencode.db 2>/dev/null)" = "$db" ] || { find /tmp "$HOME" -name opencode.db; exit 45; }
[ ! -e "$PROVEO_HOME/opencode/v2/share" ] || exit 46
ls -d /tmp/proveo-opencode-throwaway-* 2>/dev/null && exit 47
echo ENGINE-HISTORY-KEPT`

// ocInflight runs the git-sync commit-subject call through the shim and checks the engine database did not change.
// SPEC: _spec/defs/opencode/engine-local-history.puml
const ocInflight = `set -u
shim=/opt/proveo/shims/opencode
db="$HOME/.local/share/opencode/opencode.db"
mkdir -p /tmp/work && cd /tmp/work || exit 50
"$shim" session list --format json >/dev/null 2>/tmp/list.err || { cat /tmp/list.err; exit 51; }
[ -f "$db" ] || exit 52
sums() { find "$HOME/.local/share/opencode" -type f -exec sha256sum {} + | sort; }
before=$(sums)
PROVEO_GIT_SYNC_MSG_INFLIGHT=1 timeout 8 "$shim" run --standalone -m fixture/model "write a commit subject" >/tmp/inflight.log 2>&1
code=$?
[ "$code" = 0 ] || [ "$code" = 124 ] || { cat /tmp/inflight.log; exit 53; }
[ "$(sums)" = "$before" ] || { echo "$before"; sums; exit 54; }
ls -d /tmp/proveo-opencode-throwaway-* 2>/dev/null && exit 55
echo INFLIGHT-UNTOUCHED`

// ocImportFixture builds a WAL database as root and prints each file base64-encoded on a FILE<suffix>= line.
// SPEC: _spec/defs/opencode/engine-local-history.puml
const ocImportFixture = `import base64, os, sqlite3, sys, tempfile
src = os.path.join(tempfile.mkdtemp(), "opencode.db")
con = sqlite3.connect(src)
con.execute("PRAGMA journal_mode=WAL")
con.execute("PRAGMA wal_autocheckpoint=0")
con.execute("CREATE TABLE fixture (note TEXT)")
con.executemany("INSERT INTO fixture VALUES (?)", [("engine-local",), ("imported-once",)])
con.commit()
assert os.path.getsize(src + "-wal") > 0
for suffix in ("", "-wal"):
    with open(src + suffix, "rb") as f:
        print("FILE" + suffix + "=" + base64.b64encode(f.read()).decode())
sys.stdout.flush()
os._exit(0)`

// ocImport stages the host's import stream as root with mode 0600, then imports it twice and
// exports it as uid 1000, the engine user `sbx exec` runs.
// SPEC: _spec/defs/opencode/engine-local-history.puml
const ocImport = `set -u
db=/home/agent/.local/share/opencode/opencode.db
stage=/tmp/stage.tar
as_agent() { HOME=/home/agent python3 -I -c 'import os, sys
os.setgroups([]); os.setgid(1000); os.setuid(1000)
os.execvp(sys.argv[1], sys.argv[1:])' "$@"; }
notes() { python3 -c 'import sqlite3, sys; print(",".join(r[0] for r in sqlite3.connect(sys.argv[1]).execute("SELECT note FROM fixture ORDER BY rowid")))' "$1"; }
[ "$(id -u)" = 0 ] || exit 60
[ ! -e "$db" ] || exit 61
(umask 077 && cat > "$stage") || exit 62
[ "$(stat -c '%u %a' "$stage")" = "0 600" ] || exit 63
as_agent test -r "$stage" && exit 63
as_agent python3 -I -c "$IMPORT_SCRIPT" < "$stage"; code=$?
[ "$code" = 0 ] || { echo "import exit=$code"; exit 64; }
[ "$(stat -c '%u %a' "$db")" = "1000 600" ] || { stat -c '%u %a' "$db"; exit 65; }
before=$(sha256sum "$db")
as_agent python3 -I -c "$IMPORT_SCRIPT" < "$stage"; code=$?
[ "$code" = "$IMPORT_EXISTING" ] || { echo "second import exit=$code"; exit 66; }
[ "$(sha256sum "$db")" = "$before" ] || exit 67
ls -d /tmp/proveo-import-* 2>/dev/null && exit 68
echo "IMPORTED=$(notes "$db")"
as_agent python3 -I -c "$EXPORT_SCRIPT" >/tmp/export.db || exit 69
echo "HEADER=$(od -An -tx1 -N16 /tmp/export.db | tr -d ' \n')"
echo "EXPORTED=$(notes /tmp/export.db)"`

// ocImportStream is the host's import stream for the WAL database ocImportFixture printed in out.
func ocImportStream(t *testing.T, out string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "opencode.db")
	for _, suffix := range []string{"", "-wal"} {
		var body []byte
		for l := range strings.SplitSeq(out, "\n") {
			if enc, ok := strings.CutPrefix(l, "FILE"+suffix+"="); ok {
				b, err := base64.StdEncoding.DecodeString(enc)
				if err != nil {
					t.Fatalf("fixture FILE%s: %v", suffix, err)
				}
				body = b
			}
		}
		if len(body) == 0 {
			t.Fatalf("the fixture printed no FILE%s line:\n%s", suffix, out)
		}
		ocWrite(t, src+suffix, string(body), 0o600)
	}
	var stream bytes.Buffer
	if err := sbx.WriteOpenCodeImport(&stream, src, sbx.OpenCodeImportFiles(src)); err != nil {
		t.Fatal(err)
	}
	return stream.String()
}

func ocWrite(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func ocHasLine(out, line string) bool {
	for l := range strings.SplitSeq(out, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

func ocClip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func TestImageOpencode(t *testing.T) {
	image := imagetest.Resolve("opencode", "proveo/opencode:latest")
	s := imagetest.New(t, image)

	ocBuild(s)
	ocTools(s)
	ocSecurity(s)
	ocConfig(s)
	ocDefaults(s)
	ocHistory(s)
	ocMCP(s)
	ocLLM(s)
}

func ocBuild(s *imagetest.Suite) {
	img := s.Image
	s.Check("image is available", func(t *testing.T) {
		if !imagetest.Present(img) {
			t.Fatalf("image %s not present", img)
		}
	})
	s.Inspect("has security.non-root=true label", img, `{{index .Config.Labels "security.non-root"}}`, "true")
	s.Inspect("has security.hardened=true label", img, `{{index .Config.Labels "security.hardened"}}`, "true")
	s.Inspect("Docker USER is non-root (opencode)", img, `{{.Config.User}}`, "opencode")
	s.Inspect("entrypoint uses dumb-init", img, `{{json .Config.Entrypoint}}`, "dumb-init")

	// SPEC: _spec/_devops/agent-version-pin.puml
	s.Inspect("proveo.agent label names the agent package", img, `{{index .Config.Labels "proveo.agent"}}`, "@opencode/cli")
	r := imagetest.Docker(imagetest.DefaultTimeout, nil, "image", "inspect", "-f", `{{index .Config.Labels "proveo.agent.version"}}`, img)
	label := ""
	if r.OK() {
		label = strings.TrimRight(r.Out, "\n")
	}
	if label != "" {
		s.Matches("opencode --version matches proveo.agent.version="+label, img, "opencode --version", `^opencode v`+regexp.QuoteMeta(label)+`$`)
	} else {
		s.Check("proveo.agent.version label is set", func(t *testing.T) {
			t.Error("image predates the pin — proveo build opencode")
		})
	}

	// SPEC: _spec/packages/lib/seed-and-launch.puml
	s.Success("ships the Kit's startup command (/usr/local/bin/proveo-seed)", img, "test -x /usr/local/bin/proveo-seed")
}

func ocTools(s *imagetest.Suite) {
	img := s.Image
	tools := []struct{ name, cmd string }{
		{"opencode", "opencode --version"},
		{"node", "node --version"},
		{"npm", "npm --version"},
		{"pnpm", "timeout 10s pnpm --version"},
		{"bun", "bun --version"},
		{"bunx", "bunx --version"},
		{"git", "git --version"},
		{"ffmpeg", "ffmpeg -hide_banner -version"},
		{"gh", "gh --version"},
		{"curl", "curl --version"},
		{"dumb-init", "dumb-init --version"},
	}
	for _, tl := range tools {
		s.Success(tl.name+" is installed", img, tl.cmd)
	}

	s.Matches("node version is v22.x", img, "node --version", `^v22\.`)
	s.Contains("bun executes a TypeScript file without a build step", img,
		"printf 'const n: number = 21; console.log(n * 2)' > /tmp/x.ts && bun /tmp/x.ts", "42")
	s.Contains("opencode CLI exposes 'run' subcommand", img, "opencode --help 2>&1", "run")

	browser := imagetest.Resolve("opencode-browser", "proveo/opencode-browser:latest")
	if !imagetest.Present(browser) {
		s.Skip("[browser] opencode-browser variant", "image "+browser+" not built (mise run build opencode-browser)")
		return
	}
	s.Success("[browser] playwright CLI is installed", browser, "playwright --version")
	s.Success("[browser] agent-browser is installed", browser, "agent-browser --version")
	s.Success("[browser] agent-browser points at Playwright's Chromium (no second download)", browser,
		`test -x "$AGENT_BROWSER_EXECUTABLE_PATH" && readlink -f "$AGENT_BROWSER_EXECUTABLE_PATH" | grep -q "^${PLAYWRIGHT_BROWSERS_PATH}/chromium-"`)
	s.Contains("[browser] agent-browser serves its bundled skills", browser, "agent-browser skills list", "core")
	s.Contains("[browser] the seed drops the skill into ~/.config/opencode/skills", browser,
		`export HOME=/tmp; source /entrypoint-lib.sh; proveo_seed_browser_skills opencode >/dev/null; head -2 /tmp/.config/opencode/skills/agent-browser/SKILL.md`,
		"name: agent-browser")
	s.Contains("[browser] agent-browser drives a headless Chromium as the image user", browser,
		`export HOME=/tmp AGENT_BROWSER_SOCKET_DIR=/tmp/ab; mkdir -p /tmp/ab; agent-browser open about:blank >/dev/null && agent-browser get url; agent-browser close >/dev/null`,
		"about:blank")
}

func ocSecurity(s *imagetest.Suite) {
	img := s.Image
	s.Contains("runs as user opencode", img, "whoami", "opencode")

	uid := os.Getenv("EXPECTED_UID")
	if uid == "" {
		uid = "1000"
	}
	s.Contains("UID is "+uid, img, "id -u", uid)

	s.Failure("no setuid binaries", img, "find / -xdev -perm -4000 -type f 2>/dev/null | grep -q .")
	s.Failure("no setgid binaries", img, "find / -xdev -perm -2000 -type f 2>/dev/null | grep -q .")
	s.Failure("nc not available", img, "which nc")
	s.Failure("netcat not available", img, "which netcat")
	s.Failure("netstat not available", img, "which netstat")
	s.Failure("ss not available", img, "which ss")
	s.Failure("cannot write to /usr/bin", img, "touch /usr/bin/testfile 2>/dev/null")
	s.Failure("cannot write to /etc", img, "touch /etc/testfile 2>/dev/null")
	s.Contains("v2 auto-update is disabled", img, `echo $OPENCODE_DISABLE_AUTOUPDATE`, "1")
	s.Success("native YOLO and visible execution preferences are baked", img,
		`printf '%s' "$OPENCODE_CLI_CONFIG_CONTENT" | jq -e '
          .session.permissions == "autoaccept" and .session.markdown == "source"
          and .session.thinking == "show" and .session.grouping == "none"
          and .session.verbosity == "high" and .session.sidebar == "auto"
          and .tabs.mode == "on" and .tabs.indicators == "status"
          and .mini.tools == "show" and .mini.thinking == "show"
          and .mini.shell_output == "show" and .mini.turn_summary == "show"
          and .mini.footer == "show"'`)

	s.Check("arbitrary --user uid gets usable identity and writable HOME", func(t *testing.T) {
		r := ocRun(t, imagetest.DefaultTimeout, nil, "--user", "4242:4242", "--entrypoint", "bash", img, "-c",
			`source /entrypoint-lib.sh && proveo-entrypoint prep >/dev/null && echo "uid=$(id -u) home_writable=$(test -w "$HOME" && echo yes || echo no)"`)
		if !strings.Contains(r.Out, "uid=4242 home_writable=yes") {
			t.Errorf("output: %s", ocClip(r.Out, 200))
		}
	})

	s.Failure("does not run as root by default", img, `[ "$(id -u)" = "0" ]`)
}

func ocConfig(s *imagetest.Suite) {
	img := s.Image
	fixture := s.T.TempDir()
	ocWrite(s.T, filepath.Join(fixture, "opencode.json"), `{
  "$schema": "https://opencode.ai/config.json",
  "model": "anthropic/claude-sonnet-4-5"
}
`, 0o644)
	ocWrite(s.T, filepath.Join(fixture, "AGENTS.md"), "# Test fixture agents file.\n", 0o644)
	ocWrite(s.T, filepath.Join(fixture, ".env"), "OPENCODE_SMALL_MODEL=from-workspace-env\n", 0o644)
	mount := fixture + ":/app"

	s.Check("entrypoint detects opencode.json + AGENTS.md", func(t *testing.T) {
		r := ocRun(t, 30*time.Second, nil, append(ocNoInstalls, "-v", mount, "--entrypoint", "/entrypoint.sh", img, "--version")...)
		for _, want := range []string{"Found opencode.json", "Found AGENTS.md"} {
			if !strings.Contains(r.Out, want) {
				t.Errorf("entrypoint detects config (missing %q; output: %s)", want, ocClip(r.Out, 300))
			}
		}
	})

	ocWrite(s.T, filepath.Join(fixture, "fake-bin", "opencode"), `#!/usr/bin/env bash
printf 'SAW OPENCODE_MODEL=[%s]\n' "${OPENCODE_MODEL:-}"
printf 'SAW OPENCODE_SMALL_MODEL=[%s]\n' "${OPENCODE_SMALL_MODEL:-}"
printf 'SAW OPENCODE_BUILD_MODEL=[%s]\n' "${OPENCODE_BUILD_MODEL:-}"
printf 'SAW SMALL_MODEL=[%s]\n' "${SMALL_MODEL:-}"
if [[ "${1:-}" == "--version" ]]; then
  echo "9.9.9"
fi
`, 0o755)
	fakeRun := func(t *testing.T, env ...string) string {
		args := append(append(append([]string{}, ocNoInstalls...), env...), "-v", mount, "--entrypoint", "bash", img, "-c",
			`PATH="/app/fake-bin:$PATH" /entrypoint.sh --version`)
		return ocRun(t, 30*time.Second, nil, args...).Out
	}

	s.Check("the workspace .env never reaches opencode", func(t *testing.T) {
		out := fakeRun(t)
		if !strings.Contains(out, "SAW OPENCODE_SMALL_MODEL=[]") || strings.Contains(out, "from-workspace-env") {
			t.Errorf("the entrypoint exported the workspace .env (output: %s)", ocClip(out, 300))
		}
	})

	// SPEC: _spec/internal/entrypoint/model-alias-bridges.puml
	s.Check("a role name in the environment does NOT become an opencode model var", func(t *testing.T) {
		out := fakeRun(t, "-e", "ARCHITECT_MODEL=gpt-5.5", "-e", "EDITOR_MODEL=xai/grok-4.3", "-e", "SMALL_MODEL=xai/grok-small")
		for _, want := range []string{"SAW OPENCODE_MODEL=[]", "SAW OPENCODE_SMALL_MODEL=[]", "SAW OPENCODE_BUILD_MODEL=[]"} {
			if !strings.Contains(out, want) {
				t.Errorf("model bridging has grown back (missing %q; output: %s)", want, ocClip(out, 300))
			}
		}
	})

	s.Check("opencode's own OPENCODE_SMALL_MODEL survives and is not published as SMALL_MODEL", func(t *testing.T) {
		out := fakeRun(t, "-e", "OPENCODE_SMALL_MODEL=xai/grok-4.3")
		for _, want := range []string{"SAW OPENCODE_SMALL_MODEL=[xai/grok-4.3]", "SAW SMALL_MODEL=[]"} {
			if !strings.Contains(out, want) {
				t.Errorf("reverse bridge or own-var loss (missing %q; output: %s)", want, ocClip(out, 300))
			}
		}
	})

	s.Check("entrypoint forwards args to opencode (--version)", func(t *testing.T) {
		r := ocRun(t, 30*time.Second, nil, append(ocNoInstalls, img, "--version")...)
		if !regexp.MustCompile(`[0-9]+\.[0-9]+`).MatchString(r.Out) {
			t.Errorf("entrypoint forwards args (output: %s)", ocClip(r.Out, 300))
		}
	})
}

func ocDefaults(s *imagetest.Suite) {
	img := s.Image
	s.Success("baked defaults: opencode.json present in /opt", img, "test -f /opt/opencode/defaults/opencode.json")
	s.Success("git-sync turn hook is executable", img, "test -x /opt/proveo/hooks/git-sync-turn.sh")
	s.Success("git-sync idle plugin is baked", img, "test -f /opt/proveo/hooks/proveo-git-sync-turn.js")

	// SPEC: _spec/defs/agent-definition-sharing.puml
	s.Success("baked subagents: every agent in the opencode roster has a body in /opt", img, `set -eu
   roster="$(jq -r ".opencode[]" /opt/proveo/subagents/_roster.json)"
   [ -n "$roster" ] || { echo "opencode roster is empty"; exit 1; }
   for a in $roster; do
     test -f "/opt/proveo/subagents/$a.md" || { echo "missing /opt/proveo/subagents/$a.md"; exit 1; }
   done
   echo "roster: $(echo $roster | tr "\n" " ")"`)

	s.Success("default opencode.json: build agent asks before shell execution", img,
		`jq -e '.agents.build.permissions | any(.action == "shell" and .resource == "*" and .effect == "ask")' /opt/opencode/defaults/opencode.json`)
	s.Success("default opencode.json: plan agent denies shell execution", img,
		`jq -e '.agents.plan.permissions | any(.action == "shell" and .resource == "*" and .effect == "deny")' /opt/opencode/defaults/opencode.json`)
	s.Success("default opencode.json: automatic compaction enabled", img,
		`jq -e '.compaction.auto == true' /opt/opencode/defaults/opencode.json`)

	// SPEC: _spec/internal/entrypoint/model-alias-bridges.puml
	for _, seed := range []string{"/opt/opencode/defaults/opencode.json", "/opt/opencode/sample_opencode.json"} {
		s.Success(filepath.Base(seed)+": names no model by environment", img, "! grep -q 'env:OPENCODE' "+seed)
	}
	s.Success("default opencode.json: plan and build omit their own model (inherit the global)", img,
		"jq -e '.agents.plan.model == null and .agents.build.model == null and .model == null' /opt/opencode/defaults/opencode.json")

	s.Check("entrypoint seeds ~/.config/opencode on first run", func(t *testing.T) {
		r := ocRun(t, imagetest.DefaultTimeout, nil, "--entrypoint", "/entrypoint.sh", img, "--version")
		if !regexp.MustCompile(`(Seeded global defaults|already-seeded|opencode version)`).MatchString(r.Out) {
			t.Fatalf("entrypoint did not run (output: %s)", ocClip(r.Out, 300))
		}
		c := ocRun(t, imagetest.DefaultTimeout, nil, "--entrypoint", "bash", img, "-c",
			`/entrypoint.sh --version >/dev/null 2>&1; test -f "$HOME/.config/opencode/opencode.json" && test -f "$HOME/.config/opencode/agents/adversarial-reviewer.md" && echo OK`)
		if !ocHasLine(c.Out, "OK") {
			t.Errorf("seed check (output: %s)", ocClip(c.Out, 300))
		}
	})

	s.Check("OPENCODE_RESEED=1 overwrites existing config", func(t *testing.T) {
		r := ocRun(t, imagetest.DefaultTimeout, nil, "-e", "OPENCODE_RESEED=1", "--entrypoint", "bash", img, "-c", `
    mkdir -p "$HOME/.config/opencode"
    echo "{ \"model\": \"DIRTY\" }" > "$HOME/.config/opencode/opencode.json"
    /entrypoint.sh --version >/dev/null 2>&1
    grep -q "DIRTY" "$HOME/.config/opencode/opencode.json" && echo NOT_RESEEDED || echo RESEEDED
  `)
		if !ocHasLine(r.Out, "RESEEDED") {
			t.Errorf("OPENCODE_RESEED behaviour (output: %s)", ocClip(r.Out, 300))
		}
	})

	s.Check("entrypoint preserves existing opencode.json (no reseed)", func(t *testing.T) {
		r := ocRun(t, imagetest.DefaultTimeout, nil, "--entrypoint", "bash", img, "-c", `
    mkdir -p "$HOME/.config/opencode"
    echo "{ \"model\": \"USER_CUSTOM\" }" > "$HOME/.config/opencode/opencode.json"
    /entrypoint.sh --version >/dev/null 2>&1
    grep -q "USER_CUSTOM" "$HOME/.config/opencode/opencode.json" && echo PRESERVED || echo CLOBBERED
  `)
		if !ocHasLine(r.Out, "PRESERVED") {
			t.Errorf("preserve behaviour (output: %s)", ocClip(r.Out, 300))
		}
	})
}

// SPEC: _spec/defs/opencode/engine-local-history.puml
func ocHistory(s *imagetest.Suite) {
	img := s.Image
	engine := []string{"--network", "none", "-e", "SANDBOX_VM_ID=imagetest-engine", "-e", "PROVEO_HOME=/tmp/home",
		"-e", "PROVEO_STATE_HOME=/tmp/home", "-e", "OPENCODE_DISABLE_MODELS_FETCH=1", "-e", "OPENCODE_CONFIG_CONTENT=" + ocFixtureProvider}

	s.Check("two shim launches share the native default database and create no V2 share", func(t *testing.T) {
		r := ocRun(t, 120*time.Second, nil, append(append(engine, ocNoInstalls...), "--entrypoint", "bash", img, "-c", ocEngineHistory)...)
		if !r.OK() || !ocHasLine(r.Out, "ENGINE-HISTORY-KEPT") {
			t.Fatalf("engine history (exit codes 40-47 name the failing step): %v\n%s", r.Err, r.Out)
		}
	})

	s.Check("the git-sync inflight call leaves the engine database untouched", func(t *testing.T) {
		r := ocRun(t, 120*time.Second, nil, append(append(engine, ocNoInstalls...), "--entrypoint", "bash", img, "-c", ocInflight)...)
		if !r.OK() || !ocHasLine(r.Out, "INFLIGHT-UNTOUCHED") {
			t.Fatalf("inflight launch (exit codes 50-55 name the failing step): %v\n%s", r.Err, r.Out)
		}
	})

	s.Check("uid 1000 imports a root-staged 0600 WAL stream once and the export streams it", func(t *testing.T) {
		fixture := ocRun(t, 60*time.Second, nil, "--network", "none", "--user", "0:0", "--entrypoint", "python3", img, "-I", "-c", ocImportFixture)
		if !fixture.OK() {
			t.Fatalf("fixture: %v\n%s", fixture.Err, fixture.Out)
		}
		r := ocRunStdin(t, 60*time.Second, nil, ocImportStream(t, fixture.Out), "--network", "none", "--user", "0:0",
			"-e", "IMPORT_EXISTING="+strconv.Itoa(sbx.OpenCodeImportExisting),
			"-e", "IMPORT_SCRIPT="+sbx.OpenCodeImportScript,
			"-e", "EXPORT_SCRIPT="+sbx.OpenCodeExportScript,
			"--entrypoint", "bash", img, "-c", ocImport)
		if !r.OK() {
			t.Fatalf("import sequence (exit codes 60-69 name the failing step): %v\n%s", r.Err, r.Out)
		}
		for _, want := range []string{
			"IMPORTED=engine-local,imported-once",
			"HEADER=" + hex.EncodeToString([]byte(sbx.OpenCodeDatabaseHeader)),
			"EXPORTED=engine-local,imported-once",
		} {
			if !ocHasLine(r.Out, want) {
				t.Errorf("missing %q:\n%s", want, r.Out)
			}
		}
	})
}

func ocMCP(s *imagetest.Suite) {
	img := s.Image
	fixture := s.T.TempDir()
	ocWrite(s.T, filepath.Join(fixture, "opencode.json"), `{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "fs-test": {
        "type": "local",
        "command": ["node", "/app/mcp-fixture.cjs"]
      }
    }
  }
}
`, 0o644)
	ocWrite(s.T, filepath.Join(fixture, "mcp-fixture.cjs"), `const fs = require("node:fs")
const readline = require("node:readline")
readline.createInterface({ input: process.stdin }).on("line", (line) => {
  const request = JSON.parse(line)
  if (request.id === undefined) return
  let result = {}
  switch (request.method) {
    case "initialize":
      result = { protocolVersion: request.params.protocolVersion, capabilities: { tools: {} }, serverInfo: { name: "fs-test", version: "1.0.0" } }
      break
    case "tools/list":
      result = { tools: [{ name: "read_file", description: "Read a fixture file", inputSchema: { type: "object", properties: { path: { type: "string" } }, required: ["path"] } }] }
      break
    case "tools/call":
      result = { content: [{ type: "text", text: fs.readFileSync(request.params.arguments.path, "utf8") }] }
      break
  }
  process.stdout.write(JSON.stringify({ jsonrpc: "2.0", id: request.id, result }) + "\n")
}).on("close", () => process.exit(0))
`, 0o644)
	ocWrite(s.T, filepath.Join(fixture, "marker.txt"), "MCP_FIXTURE_OK\n", 0o644)
	mount := fixture + ":/app"

	s.Check("opencode discovers MCP server from opencode.json", func(t *testing.T) {
		r := ocRun(t, 90*time.Second, nil, "--network", "none", "-v", mount, "-w", "/app",
			"-e", "OPENCODE_SERVER_PASSWORD=offline-mcp", "--entrypoint", "bash", img, "-c", `set -euo pipefail
timeout 60 opencode serve --hostname 127.0.0.1 --port 4096 >/tmp/mcp-serve.log 2>&1 & server=$!
cleanup() {
  local code=$? server_code
  trap - EXIT
  if kill -0 "$server" 2>/dev/null; then kill "$server"; fi
  if wait "$server"; then
    :
  else
    server_code=$?
    if [[ "$server_code" != 143 ]]; then
      printf 'MCP server exited with status %s\n' "$server_code" >&2
      code=1
    fi
  fi
  if ((code != 0)); then cat /tmp/mcp-serve.log >&2; fi
  exit "$code"
}
trap cleanup EXIT
config=''
for ((i=0;i<100;i++)); do
  if config="$(curl -fsS --max-time 2 -u opencode:offline-mcp 'http://127.0.0.1:4096/api/config?location%5Bdirectory%5D=%2Fapp' 2>/dev/null)"; then break; fi
  sleep 0.2
done
printf 'MCP_CONFIG=%s\n' "$config"
jq -e --slurpfile expected /app/opencode.json 'any(.[]; .type == "document" and .path == "/app/opencode.json" and .info == $expected[0])' <<< "$config" >/dev/null
for ((i=0;i<100;i++)); do
  mcp="$(curl -fsS --max-time 2 -u opencode:offline-mcp 'http://127.0.0.1:4096/api/mcp?location%5Bdirectory%5D=%2Fapp')"
  if jq -e '.location.directory == "/app" and any(.data[]; .name == "fs-test" and .status.status == "connected")' <<< "$mcp" >/dev/null; then
    printf 'MCP_STATUS=%s\n' "$mcp"
    printf 'MCP_CONNECTED=fs-test\n'
    exit 0
  fi
  sleep 0.2
done
printf 'MCP did not connect: %s\n' "$mcp" >&2
exit 1`)
		if !r.OK() || !ocHasLine(r.Out, "MCP_CONNECTED=fs-test") {
			t.Errorf("MCP discovery: exit=%d, error=%v\n%s", r.Code, r.Err, r.Out)
		}
	})

	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		s.Skip("opencode can invoke MCP tool end-to-end", "no ANTHROPIC_API_KEY")
		return
	}
	s.Check("opencode invokes MCP tool end-to-end", func(t *testing.T) {
		r := ocRun(t, 200*time.Second, nil, "-v", mount, "-w", "/app", "-e", "ANTHROPIC_API_KEY="+key,
			"--entrypoint", "bash", img, "-c",
			`cd /app && timeout 180 opencode run -m anthropic/claude-sonnet-4-5 "Use the fs-test MCP server to read /app/marker.txt and reply with its exact contents only." 2>&1`)
		if !r.OK() || !strings.Contains(r.Out, "MCP_FIXTURE_OK") {
			t.Errorf("MCP tool invocation: exit=%d, error=%v (output: %s)", r.Code, r.Err, ocClip(r.Out, 500))
		}
	})
}

func ocLLM(s *imagetest.Suite) {
	img := s.Image
	providers := []struct{ provider, model, envvar string }{
		{"anthropic", "anthropic/claude-haiku-4-5", "ANTHROPIC_API_KEY"},
		{"openai", "openai/gpt-4.1-mini", "OPENAI_API_KEY"},
		{"openrouter", "openrouter/anthropic/claude-3.5-haiku", "OPENROUTER_API_KEY"},
		{"xai", "xai/grok-2-1212", "XAI_API_KEY"},
		{"google", "google/gemini-2.0-flash", "GEMINI_API_KEY"},
		{"groq", "groq/llama-3.1-8b-instant", "GROQ_API_KEY"},
		{"deepseek", "deepseek/deepseek-chat", "DEEPSEEK_API_KEY"},
	}
	for _, p := range providers {
		desc := "[" + p.provider + "] opencode run completes via " + p.envvar
		key := os.Getenv(p.envvar)
		if key == "" {
			s.Skip(desc, "no "+p.envvar)
			continue
		}
		s.Check(desc, func(t *testing.T) {
			r := ocRun(t, 150*time.Second, nil, "-e", p.envvar+"="+key, "--entrypoint", "bash", img, "-c",
				"timeout 120 opencode run -m '"+p.model+"' 'Respond with only the word PONG.' 2>&1")
			if !strings.Contains(strings.ToUpper(r.Out), "PONG") {
				t.Errorf("[%s] opencode run (output: %s)", p.provider, ocClip(r.Out, 300))
			}
		})
	}
}
