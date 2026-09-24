//go:build image

// SPEC: _spec/tests/testing-strategy.puml, _spec/_plans/host-shell-to-go.puml
package imagetest_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/imagetest"
)

const cursorFakeAgent = `#!/usr/bin/env bash
case "${1:-}" in
  --version|-v) echo "1.0.0"; exit 0 ;;
esac
model=""
while [[ $# -gt 0 ]]; do
  if [[ "$1" == "--model" ]]; then
    shift
    model="$1"
    break
  fi
  shift
done
echo "PASSED_MODEL=${model}"
`

// cursorRun is `run_timeout <d> docker run --rm <args...>`, output combined.
func cursorRun(d time.Duration, args ...string) string {
	return imagetest.Docker(d, nil, append([]string{"run", "--rm"}, args...)...).Out
}

func cursorLine(out, pattern string) bool {
	return regexp.MustCompile("(?m)" + pattern).MatchString(out)
}

func cursorExpect(t *testing.T, ok bool, out string) {
	t.Helper()
	if !ok {
		if len(out) > 300 {
			out = out[:300]
		}
		t.Errorf("output: %s", out)
	}
}

func TestImageCursor(t *testing.T) {
	image := imagetest.Resolve("IMAGE", "proveo/cursor:latest")
	s := imagetest.New(t, image)

	// Phase 1: Build
	s.Check("image is available", func(t *testing.T) {
		if !imagetest.Present(image) && !imagetest.Docker(0, nil, "pull", image).OK() {
			t.Fatalf("cannot continue without image %s", image)
		}
	})
	s.Inspect("has security.non-root=true label", image, `{{index .Config.Labels "security.non-root"}}`, "true")
	s.Inspect("has security.hardened=true label", image, `{{index .Config.Labels "security.hardened"}}`, "true")
	s.Inspect("proveo.agent label names the agent package", image, `{{index .Config.Labels "proveo.agent"}}`, "cursor-agent")
	version := strings.TrimSpace(imagetest.Docker(0, nil, "image", "inspect", "-f", `{{index .Config.Labels "proveo.agent.version"}}`, image).Out)
	if version != "" {
		s.Success("installed cursor-agent release is proveo.agent.version="+version, image,
			"test -d /opt/cursor-dist/.local/share/cursor-agent/versions/"+version)
	} else {
		s.Check("proveo.agent.version label is set", func(t *testing.T) {
			t.Error("image predates the pin — proveo build cursor")
		})
	}
	s.Inspect("Docker USER is non-root (cursor)", image, `{{.Config.User}}`, "cursor")
	s.Inspect("entrypoint uses dumb-init", image, `{{json .Config.Entrypoint}}`, "dumb-init")
	s.Success("ships the Kit's startup command (/usr/local/bin/proveo-seed)", image, "test -x /usr/local/bin/proveo-seed")

	// Phase 2: Tool Verification
	s.Success("cursor cli (agent) is installed and reports a version", image, "agent --version")
	s.Success("legacy cursor-agent alias resolves", image, "command -v cursor-agent")
	s.Matches("agent binary lives under the root-owned dist prefix", image, "readlink -f /usr/local/bin/agent", "^/opt/cursor-dist/")
	s.Success("git is installed", image, "git --version")
	s.Success("ffmpeg is installed", image, "ffmpeg -hide_banner -version")
	s.Success("gh is installed", image, "gh --version")
	s.Failure("bun stays out of the runtime-free cursor image (it lives in proveo/base-node)", image, "command -v bun")
	s.Success("docker client comes from the sandbox template", image, "command -v docker")
	s.Success("docker Engine comes from the -docker template variant", image, "command -v dockerd")
	s.Failure("harden pass leaves no setuid binary, sudo included", image,
		"find / -xdev -perm -4000 -type f 2>/dev/null | grep -q .")
	s.Success("shared verification lib is baked", image,
		`command -v proveo-entrypoint >/dev/null || test -f /opt/proveo/lib/detect-verify.sh`)

	browser := imagetest.Resolve("CURSOR_BROWSER_IMAGE", "proveo/cursor-browser:latest")
	if imagetest.Present(browser) {
		s.Success("[browser] playwright CLI is installed", browser, "playwright --version")
		s.Success("[browser] agent-browser is installed", browser, "agent-browser --version")
		s.Success("[browser] agent-browser points at Playwright's Chromium (no second download)", browser,
			`test -x "$AGENT_BROWSER_EXECUTABLE_PATH" && readlink -f "$AGENT_BROWSER_EXECUTABLE_PATH" | grep -q "^${PLAYWRIGHT_BROWSERS_PATH}/chromium-"`)
		s.Contains("[browser] agent-browser serves its bundled skills", browser, "agent-browser skills list", "core")
		s.Contains("[browser] the seed drops the skill into ~/.cursor/skills", browser,
			`export HOME=/tmp; source /entrypoint-lib.sh; proveo_seed_browser_skills cursor >/dev/null; head -2 /tmp/.cursor/skills/agent-browser/SKILL.md`,
			"name: agent-browser")
		s.Contains("[browser] agent-browser drives a headless Chromium as the image user", browser,
			`export HOME=/tmp AGENT_BROWSER_SOCKET_DIR=/tmp/ab; mkdir -p /tmp/ab; agent-browser open about:blank >/dev/null && agent-browser get url; agent-browser close >/dev/null`,
			"about:blank")
		s.Failure("[browser] Claude in Chrome relay is claudecode's alone", browser, "test -f /opt/proveo/lib/chrome-bridge.js")
	} else {
		s.Skip("[browser] cursor-browser variant", "image "+browser+" not built (mise run build cursor-browser)")
	}

	// Phase 3: Security Hardening
	s.Contains("container runs as the cursor user", image, "whoami", "cursor")
	s.Failure("nc is not present", image, "command -v nc")
	s.Failure("netcat is not present", image, "command -v netcat")
	s.Check("no setuid binaries remain", func(t *testing.T) {
		out := strings.TrimRight(cursorRun(0, "--entrypoint", "bash", image, "-c", "find / -xdev -perm -4000 -type f 2>/dev/null"), "\n")
		if out != "" {
			t.Errorf("setuid files: %s", out)
		}
	})
	s.Success("enterprise hooks.json exists", image, "test -f /etc/cursor/hooks.json")
	s.Failure("enterprise hooks.json is not writable by the runtime user", image, "test -w /etc/cursor/hooks.json")
	s.Failure("cursor dist prefix is not writable by the runtime user (no self-update/tamper)", image, "test -w /opt/cursor-dist/.local/bin")

	// Phase 4: Configuration & Entrypoint
	smoke := cursorRun(60*time.Second, "-e", "PROVEO_SMOKE_TEST=1", "--entrypoint", "bash", image, "-c", "timeout 10 /entrypoint.sh; true")
	s.Check("smoke mode prints PROVEO_SMOKE_READY", func(t *testing.T) {
		cursorExpect(t, strings.Contains(smoke, "PROVEO_SMOKE_READY cursor"), smoke)
	})
	s.Check("preamble states the paradigm", func(t *testing.T) {
		cursorExpect(t, strings.Contains(smoke, "policy-gated autonomous loop"), smoke)
	})
	s.Check("preamble reports the deny-rule baseline", func(t *testing.T) {
		cursorExpect(t, strings.Contains(smoke, "Deny rules (survive --force)"), smoke)
	})
	s.Check("preamble lists seeded subagents", func(t *testing.T) {
		cursorExpect(t, strings.Contains(smoke, "Subagents available:"), smoke)
	})
	s.Check("proxy env sets useHttp1ForAgent=true in seeded config", func(t *testing.T) {
		out := cursorRun(60*time.Second, "-e", "HTTPS_PROXY=http://squid:3128", "--entrypoint", "bash", image, "-c",
			`/entrypoint.sh --version >/dev/null 2>&1; grep -c "\"useHttp1ForAgent\": true" "$HOME/.cursor/cli-config.json"`)
		cursorExpect(t, cursorLine(out, "^1$"), out)
	})
	s.Check("no proxy env leaves useHttp1ForAgent disabled", func(t *testing.T) {
		out := cursorRun(60*time.Second, "--entrypoint", "bash", image, "-c",
			`/entrypoint.sh --version >/dev/null 2>&1; grep -c "\"useHttp1ForAgent\": true" "$HOME/.cursor/cli-config.json" || true`)
		cursorExpect(t, cursorLine(out, "^0$"), out)
	})

	fixture := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fixture, "fake-bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "fake-bin", "agent"), []byte(cursorFakeAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	launch := func(env string) string {
		if err := os.WriteFile(filepath.Join(fixture, ".env"), []byte(env), 0o644); err != nil {
			return err.Error()
		}
		return cursorRun(30*time.Second, "-v", fixture+":/app", "--entrypoint", "bash", image, "-c",
			`PATH="/app/fake-bin:$PATH" /entrypoint.sh -p "test"`)
	}
	s.Check("no role name in .env reaches cursor's --model", func(t *testing.T) {
		out := launch("ARCHITECT_MODEL=claude-sonnet-4\nEDITOR_MODEL=gpt-4.1\nSMALL_MODEL=gpt-4.1-mini\n")
		cursorExpect(t, cursorLine(out, "PASSED_MODEL=$") &&
			!strings.Contains(out, "PASSED_MODEL=claude-sonnet-4") &&
			!strings.Contains(out, "PASSED_MODEL=gpt-4.1"), out)
	})
	s.Check("cursor's own CURSOR_MODEL still reaches --model", func(t *testing.T) {
		out := launch("ARCHITECT_MODEL=claude-sonnet-4\nCURSOR_MODEL=explicit-model\n")
		cursorExpect(t, strings.Contains(out, "PASSED_MODEL=explicit-model"), out)
	})

	// Phase 5: Baked-in Defaults
	s.Success("baked defaults: cli-config.json present in /opt", image, "test -f /opt/cursor/defaults/cli-config.json")
	s.Success("baked defaults: loop rule present in /opt", image, "test -f /opt/cursor/defaults/rules/proveo-loop.mdc")
	s.Success("baked defaults: audit hook script is executable", image, "test -x /opt/cursor/defaults/hooks/audit-shell.sh")
	s.Success("baked subagents: every agent in the cursor roster has a body and is readonly", image, `set -eu
   roster="$(jq -r ".cursor[]" /opt/proveo/subagents/_roster.json)"
   [ -n "$roster" ] || { echo "cursor roster is empty"; exit 1; }
   for a in $roster; do
     test -f "/opt/proveo/subagents/$a.md" || { echo "missing /opt/proveo/subagents/$a.md"; exit 1; }
     grep -qx "readonly: true" "/opt/proveo/subagents/_frontmatter/cursor/$a.yaml" \
       || { echo "$a frontmatter is not readonly: true"; exit 1; }
   done
   echo "roster: $(echo $roster | tr "\n" " ")"`)
	s.Contains("default cli-config.json denies privilege escalation", image, "cat /opt/cursor/defaults/cli-config.json", `"Shell(sudo)"`)
	s.Contains("default cli-config.json denies env-file reads", image, "cat /opt/cursor/defaults/cli-config.json", `"Read(.env*)"`)
	s.Contains("enterprise hooks.json wires the shell audit hook", image, "cat /etc/cursor/hooks.json", "beforeShellExecution")
	s.Contains("enterprise hooks.json wires the git-sync stop hook", image, "cat /etc/cursor/hooks.json", "git-sync-turn.sh")
	s.Success("git-sync turn hook is executable", image, "test -x /opt/proveo/hooks/git-sync-turn.sh")

	cursorEntry := func(desc, want string, env []string, cmd string) {
		s.Check(desc, func(t *testing.T) {
			args := []string{}
			for _, e := range env {
				args = append(args, "-e", e)
			}
			args = append(args, "--entrypoint", "bash", image, "-c", cmd)
			out := cursorRun(60*time.Second, args...)
			cursorExpect(t, cursorLine(out, "^"+want+"$"), out)
		})
	}
	cursorEntry("entrypoint seeds ~/.cursor on first run", "OK", nil,
		`/entrypoint.sh --version >/dev/null 2>&1; test -f "$HOME/.cursor/cli-config.json" && test -f "$HOME/.cursor/agents/adversarial-reviewer.md" && echo OK`)
	cursorEntry("CURSOR_RESEED=1 overwrites existing config", "RESEEDED", []string{"CURSOR_RESEED=1", "PROVEO_SMOKE_TEST=1"}, `
    mkdir -p "$HOME/.cursor"
    echo "{ \"version\": 1, \"permissions\": { \"deny\": [] } }" > "$HOME/.cursor/cli-config.json"
    timeout 10 /entrypoint.sh >/dev/null 2>&1
    grep -q "Shell(sudo)" "$HOME/.cursor/cli-config.json" && echo RESEEDED || echo NOT_RESEEDED
  `)
	cursorEntry("entrypoint preserves existing cli-config.json (no reseed)", "PRESERVED", []string{"PROVEO_SMOKE_TEST=1"}, `
    mkdir -p "$HOME/.cursor"
    echo "{ \"version\": 1, \"permissions\": { \"deny\": [\"Shell(USER_CUSTOM)\"] } }" > "$HOME/.cursor/cli-config.json"
    timeout 10 /entrypoint.sh >/dev/null 2>&1
    grep -q "USER_CUSTOM" "$HOME/.cursor/cli-config.json" && echo PRESERVED || echo CLOBBERED
  `)
	cursorEntry("workspace is untouched without CURSOR_SEED_RULES", "UNTOUCHED", nil, `
    /entrypoint.sh --version >/dev/null 2>&1
    test -e /app/.cursor && echo MUTATED || echo UNTOUCHED
  `)
	cursorEntry("CURSOR_SEED_RULES=1 seeds the loop rule into the workspace", "SEEDED", []string{"CURSOR_SEED_RULES=1"}, `
    /entrypoint.sh --version >/dev/null 2>&1
    test -f /app/.cursor/rules/proveo-loop.mdc && echo SEEDED || echo MISSING
  `)
	cursorEntry("audit hook logs NDJSON and allows", "HOOK_OK", nil, `
    out=$(echo "{\"command\":\"ls\"}" | /opt/cursor/defaults/hooks/audit-shell.sh)
    grep -q "{\"command\":\"ls\"}" "$HOME/.cursor/audit-shell.ndjson" && \
      [ "$out" = "{\"permission\":\"allow\"}" ] && echo HOOK_OK
  `)

	// Phase 6: Direct LLM API
	key := os.Getenv("CURSOR_API_KEY")
	if key == "" {
		s.Skip("live agent round-trip", "CURSOR_API_KEY not set")
		return
	}
	s.Check("live agent round-trip", func(t *testing.T) {
		out := imagetest.Docker(180*time.Second, []string{"CURSOR_API_KEY=" + key}, "run", "--rm", "-e", "CURSOR_API_KEY",
			"--entrypoint", "bash", image, "-c", `cd /app && agent -p --force --trust --output-format text "Reply with exactly: PROVEO_OK"`).Out
		cursorExpect(t, strings.Contains(out, "PROVEO_OK"), out)
	})
}
