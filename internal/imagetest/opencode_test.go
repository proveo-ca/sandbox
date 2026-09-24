//go:build image

// SPEC: _spec/tests/testing-strategy.puml, _spec/_plans/host-shell-to-go.puml
package imagetest_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/imagetest"
)

var ocSeq atomic.Int64

// ocRun runs `docker run --name <unique> args...` bounded by timeout and
// force-removes the container afterwards, so a timed-out client leaves nothing behind.
func ocRun(t *testing.T, timeout time.Duration, env []string, args ...string) imagetest.Result {
	t.Helper()
	name := fmt.Sprintf("proveo-imagetest-opencode-%d-%d", os.Getpid(), ocSeq.Add(1))
	t.Cleanup(func() { imagetest.Docker(30*time.Second, nil, "rm", "-f", name) })
	return imagetest.Docker(timeout, env, append([]string{"run", "--rm", "--name", name}, args...)...)
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
	image := imagetest.Resolve("IMAGE", "proveo/opencode:latest")
	s := imagetest.New(t, image)

	ocBuild(s)
	ocTools(s)
	ocSecurity(s)
	ocConfig(s)
	ocDefaults(s)
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
	s.Inspect("proveo.agent label names the agent package", img, `{{index .Config.Labels "proveo.agent"}}`, "opencode-ai")
	r := imagetest.Docker(imagetest.DefaultTimeout, nil, "image", "inspect", "-f", `{{index .Config.Labels "proveo.agent.version"}}`, img)
	label := ""
	if r.OK() {
		label = strings.TrimRight(r.Out, "\n")
	}
	if label != "" {
		s.Contains("opencode --version matches proveo.agent.version="+label, img, "opencode --version", label)
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

	browser := imagetest.Resolve("OPENCODE_BROWSER_IMAGE", "proveo/opencode-browser:latest")
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
	s.Failure("[browser] Claude in Chrome relay is claudecode's alone", browser, "test -f /opt/proveo/lib/chrome-bridge.js")
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
	s.Contains("auto-update is disabled", img, `echo $OPENCODE_AUTO_UPDATE`, "false")

	s.Check("arbitrary --user uid gets usable identity and writable HOME", func(t *testing.T) {
		r := ocRun(t, imagetest.DefaultTimeout, nil, "--user", "4242:4242", "--entrypoint", "bash", img, "-c",
			`source /entrypoint-lib.sh && ensure_runtime_user && echo "uid=$(id -u) home_writable=$(test -w "$HOME" && echo yes || echo no)"`)
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
	envFile := filepath.Join(fixture, ".env")
	ocWrite(s.T, envFile, "OPENCODE_TEST_MARKER=loaded_from_env\n", 0o644)
	mount := fixture + ":/app"

	s.Check("entrypoint detects opencode.json + AGENTS.md + .env", func(t *testing.T) {
		r := ocRun(t, 30*time.Second, nil, "-v", mount, "--entrypoint", "/entrypoint.sh", img, "--version")
		for _, want := range []string{"Found opencode.json", "Found AGENTS.md", "Loaded environment variables from .env"} {
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
	fakeRun := func(t *testing.T) string {
		return ocRun(t, 30*time.Second, nil, "-v", mount, "--entrypoint", "bash", img, "-c",
			`PATH="/app/fake-bin:$PATH" /entrypoint.sh --version`).Out
	}

	// SPEC: _spec/_plans/retire-model-bridging.puml
	ocWrite(s.T, envFile, "ARCHITECT_MODEL=gpt-5.5\nEDITOR_MODEL=xai/grok-4.3\nSMALL_MODEL=xai/grok-small\n", 0o644)
	s.Check("a role name in .env does NOT become an opencode model var", func(t *testing.T) {
		out := fakeRun(t)
		for _, want := range []string{"SAW OPENCODE_MODEL=[]", "SAW OPENCODE_SMALL_MODEL=[]", "SAW OPENCODE_BUILD_MODEL=[]"} {
			if !strings.Contains(out, want) {
				t.Errorf("model bridging has grown back (missing %q; output: %s)", want, ocClip(out, 300))
			}
		}
	})

	ocWrite(s.T, envFile, "OPENCODE_SMALL_MODEL=xai/grok-4.3\n", 0o644)
	s.Check("opencode's own OPENCODE_SMALL_MODEL survives and is not published as SMALL_MODEL", func(t *testing.T) {
		out := fakeRun(t)
		for _, want := range []string{"SAW OPENCODE_SMALL_MODEL=[xai/grok-4.3]", "SAW SMALL_MODEL=[]"} {
			if !strings.Contains(out, want) {
				t.Errorf("reverse bridge or own-var loss (missing %q; output: %s)", want, ocClip(out, 300))
			}
		}
	})

	s.Check("entrypoint forwards args to opencode (--version)", func(t *testing.T) {
		r := ocRun(t, 30*time.Second, nil, img, "--version")
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

	s.Contains("default opencode.json: build agent has bash:ask", img, "cat /opt/opencode/defaults/opencode.json", `"bash": "ask"`)
	s.Contains("default opencode.json: plan agent has bash:deny", img, "cat /opt/opencode/defaults/opencode.json", `"bash": "deny"`)
	s.Contains("default opencode.json: context rot enabled", img, "cat /opt/opencode/defaults/opencode.json", `"rot": true`)

	// SPEC: _spec/_plans/retire-model-bridging.puml
	for _, seed := range []string{"/opt/opencode/defaults/opencode.json", "/opt/opencode/sample_opencode.json"} {
		s.Success(filepath.Base(seed)+": names no model by environment", img, "! grep -q 'env:OPENCODE' "+seed)
	}
	s.Success("default opencode.json: plan and build omit their own model (inherit the global)", img,
		"jq -e '.agent.plan.model == null and .agent.build.model == null' /opt/opencode/defaults/opencode.json")

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

func ocMCP(s *imagetest.Suite) {
	img := s.Image
	fixture := s.T.TempDir()
	ocWrite(s.T, filepath.Join(fixture, "opencode.json"), `{
  "$schema": "https://opencode.ai/config.json",
  "model": "anthropic/claude-sonnet-4-5",
  "mcp": {
    "fs-test": {
      "type": "local",
      "command": ["npx", "-y", "@modelcontextprotocol/server-filesystem", "/app"],
      "enabled": true
    }
  }
}
`, 0o644)
	ocWrite(s.T, filepath.Join(fixture, "marker.txt"), "MCP_FIXTURE_OK\n", 0o644)
	mount := fixture + ":/app"

	s.Check("opencode discovers MCP server from opencode.json", func(t *testing.T) {
		r := ocRun(t, 90*time.Second, nil, "-v", mount, "-w", "/app", "--entrypoint", "bash", img, "-c",
			`cd /app && timeout 60 opencode mcp list 2>&1 || true`)
		if !regexp.MustCompile(`fs-test|filesystem`).MatchString(r.Out) {
			t.Errorf("MCP discovery (output: %s)", ocClip(r.Out, 400))
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
		if !strings.Contains(r.Out, "MCP_FIXTURE_OK") {
			t.Errorf("MCP tool invocation (output: %s)", ocClip(r.Out, 500))
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
