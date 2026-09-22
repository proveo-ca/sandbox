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

const (
	codexShort = 60 * time.Second
	codexLive  = 300 * time.Second
)

const codexFakeBin = `#!/usr/bin/env bash
case "${1:-}" in
  --version|-V) echo "codex-cli 0.0.0-fake"; exit 0 ;;
esac
echo "PASSED_ARGV=$*"
model=""
while [[ $# -gt 0 ]]; do
  if [[ "$1" == "--model" ]]; then shift; model="$1"; break; fi
  shift
done
echo "PASSED_MODEL=${model}"
`

const codexSeedProbe = `/entrypoint.sh --version >/dev/null 2>&1; `

// codexGrep reports whether some line of out matches pattern (grep semantics).
func codexGrep(out, pattern string) bool {
	return regexp.MustCompile("(?m)" + pattern).MatchString(out)
}

// codexCountLines counts lines of out containing sub (grep -c -F).
func codexCountLines(out, sub string) int {
	n := 0
	for line := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		if strings.Contains(line, sub) {
			n++
		}
	}
	return n
}

// codexRun is `docker run --rm [args...] --entrypoint bash IMAGE -c cmd`, bounded.
func codexRun(timeout time.Duration, image, cmd string, env []string, args ...string) imagetest.Result {
	argv := append([]string{"run", "--rm"}, args...)
	argv = append(argv, "--entrypoint", "bash", image, "-c", cmd)
	return imagetest.Docker(timeout, env, argv...)
}

// codexExpect fails t unless ok, showing out.
func codexExpect(t *testing.T, ok bool, what, out string) {
	t.Helper()
	if !ok {
		if len(out) > 400 {
			out = out[:400]
		}
		t.Errorf("%s (output: %s)", what, out)
	}
}

func TestImageCodex(t *testing.T) {
	image := imagetest.Resolve("IMAGE", "proveo/codex:latest")
	s := imagetest.New(t, image)

	// Phase 1: Build
	s.Check("image is available", func(t *testing.T) {
		if !imagetest.Present(image) {
			if r := imagetest.Docker(imagetest.DefaultTimeout, nil, "pull", image); !r.OK() {
				t.Fatalf("cannot pull %s: %s", image, r.Out)
			}
		}
	})
	s.Inspect("has security.non-root=true label", image, `{{index .Config.Labels "security.non-root"}}`, "true")
	s.Inspect("has security.hardened=true label", image, `{{index .Config.Labels "security.hardened"}}`, "true")
	s.Inspect("declares the sbx start-docker label", image, `{{index .Config.Labels "com.docker.sandboxes.start-docker"}}`, "true")
	s.Inspect("Docker USER is non-root (codex)", image, `{{.Config.User}}`, "codex")
	s.Inspect("entrypoint uses dumb-init", image, `{{json .Config.Entrypoint}}`, "dumb-init")
	s.Check("CODEX_HOME is not baked (it must follow $HOME)", func(t *testing.T) {
		r := imagetest.Docker(imagetest.DefaultTimeout, nil, "inspect", "--format", "{{json .Config.Env}}", image)
		codexExpect(t, !strings.Contains(r.Out, "CODEX_HOME"), "CODEX_HOME must not be baked", r.Out)
	})

	// Phase 2: Tool Verification
	s.Success("codex cli is installed and reports a version", image, "codex --version")
	s.Success("git is installed", image, "git --version")
	s.Success("gh is installed", image, "gh --version")
	s.Success("node is installed", image, "node --version")
	s.Success("docker client is installed (docker via the sbx sandbox backend)", image, "docker --version")
	s.Success("mcp-language-server bridge is baked (LSP reaches codex over MCP)", image, "command -v mcp-language-server")
	s.Success("shared verification lib is baked", image,
		`command -v proveo-entrypoint >/dev/null || test -f /opt/proveo/lib/detect-verify.sh`)
	s.Success("shared subagent frontmatter is baked", image, "test -d /opt/proveo/subagents/_frontmatter/codex")

	// Phase 3: Security Hardening
	s.Contains("container runs as the codex user", image, "whoami", "codex")
	s.Failure("nc is not present", image, "command -v nc")
	s.Failure("netcat is not present", image, "command -v netcat")
	s.Check("no setuid binaries remain", func(t *testing.T) {
		r := imagetest.ExecIn(image, "find / -xdev -perm -4000 -type f 2>/dev/null")
		out := strings.TrimRight(r.Out, "\n")
		codexExpect(t, out == "", "setuid binaries found", out)
	})
	s.Failure("baked defaults are not writable by the runtime user", image, "test -w /opt/codex/defaults/config.toml")
	s.Failure("house rules source is not writable by the runtime user", image, "test -w /opt/proveo/AGENTS.md")
	s.Success("/home/agent is a real directory (sbx mounts volumes under it)", image,
		"test -d /home/agent && test ! -L /home/agent")

	// Phase 4: Configuration & Entrypoint
	smoke := codexRun(codexShort, image, "timeout 20 /entrypoint.sh; true", nil, "-e", "PROVEO_SMOKE_TEST=1").Out
	s.Check("smoke mode prints PROVEO_SMOKE_READY", func(t *testing.T) {
		codexExpect(t, strings.Contains(smoke, "PROVEO_SMOKE_READY codex"), "smoke mode", smoke)
	})
	s.Check("preamble states the paradigm", func(t *testing.T) {
		codexExpect(t, strings.Contains(smoke, "ML blackbox algorithm"), "preamble states the paradigm", smoke)
	})
	s.Check("preamble lists composed subagents", func(t *testing.T) {
		codexExpect(t, strings.Contains(smoke, "Subagents available:"), "preamble lists composed subagents", smoke)
	})
	s.Check("warns when neither OPENAI_API_KEY nor a login exists", func(t *testing.T) {
		codexExpect(t, strings.Contains(smoke, "No credential found"), "credential warning", smoke)
	})

	fixture := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fixture, "fake-bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "fake-bin", "codex"), []byte(codexFakeBin), 0o755); err != nil {
		t.Fatal(err)
	}
	dotenv := func(t *testing.T, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(fixture, ".env"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runFake := func(entryArgs string, args ...string) string {
		cmd := `PATH="/app/fake-bin:$PATH" /entrypoint.sh ` + entryArgs
		return codexRun(codexShort, image, cmd, nil, append([]string{"-v", fixture + ":/app"}, args...)...).Out
	}
	const doThing = `"do the thing"`
	fakeContains := func(desc, want string, args ...string) {
		s.Check(desc, func(t *testing.T) {
			out := runFake(doThing, args...)
			codexExpect(t, strings.Contains(out, want), "expected to contain: "+want, out)
		})
	}

	dotenv(t, "")
	fakeContains("default launch bypasses codex's own approvals and sandbox",
		"PASSED_ARGV=--dangerously-bypass-approvals-and-sandbox")
	fakeContains("PROVEO_CODEX_SANDBOX opts back into codex's own sandbox",
		"PASSED_ARGV=--sandbox workspace-write --ask-for-approval never",
		"-e", "PROVEO_CODEX_SANDBOX=workspace-write")

	s.Check("no role name in .env reaches codex's --model", func(t *testing.T) {
		dotenv(t, "ARCHITECT_MODEL=anthropic/claude-opus-5\nEDITOR_MODEL=gpt-5.6\n")
		out := runFake(doThing)
		ok := codexGrep(out, `PASSED_MODEL=$`) &&
			!codexGrep(out, `PASSED_MODEL=claude-opus-5`) &&
			!codexGrep(out, `PASSED_MODEL=gpt-5.6`)
		codexExpect(t, ok, "model bridging has grown back", out)
	})

	s.Check("codex's own CODEX_MODEL still reaches --model", func(t *testing.T) {
		dotenv(t, "ARCHITECT_MODEL=anthropic/claude-opus-5\nCODEX_MODEL=explicit-model\n")
		out := runFake(doThing)
		codexExpect(t, strings.Contains(out, "PASSED_MODEL=explicit-model"), "expected to contain: PASSED_MODEL=explicit-model", out)
	})

	s.Check("a caller's own posture flag is left alone", func(t *testing.T) {
		dotenv(t, "")
		out := runFake("--full-auto " + doThing)
		ok := strings.Contains(out, "PASSED_ARGV=--full-auto") && !strings.Contains(out, "dangerously-bypass")
		codexExpect(t, ok, "posture flag passthrough", out)
	})

	s.Check("utility subcommands pass through unmodified", func(t *testing.T) {
		out := runFake("login")
		codexExpect(t, codexGrep(out, `PASSED_ARGV=login$`), "utility passthrough", out)
	})

	s.Check("verbose evidence puts --json after the exec subcommand", func(t *testing.T) {
		dotenv(t, "")
		out := runFake("exec " + doThing)
		want := "PASSED_ARGV=--dangerously-bypass-approvals-and-sandbox exec --json do the thing"
		codexExpect(t, strings.Contains(out, want), "--json placement", out)
	})

	s.Check("a caller's own --json is not doubled", func(t *testing.T) {
		out := runFake("exec --json " + doThing)
		codexExpect(t, codexCountLines(out, "--json") == 1, "--json doubling", out)
	})

	s.Check("PROVEO_AGENT_EVIDENCE=default adds no flags", func(t *testing.T) {
		out := runFake("exec "+doThing, "-e", "PROVEO_AGENT_EVIDENCE=default")
		codexExpect(t, !strings.Contains(out, "--json"), "evidence opt-out", out)
	})

	// Phase 5: Baked-in Defaults
	s.Success("baked defaults: config.toml present in /opt", image, "test -f /opt/codex/defaults/config.toml")
	s.Success("git-sync turn hook is executable", image, "test -x /opt/proveo/hooks/git-sync-turn.sh")
	s.Contains("default config.toml never pauses an unattended loop", image,
		"cat /opt/codex/defaults/config.toml", `approval_policy = "never"`)
	s.Contains("default config.toml leaves confinement to the container", image,
		"cat /opt/codex/defaults/config.toml", `sandbox_mode = "danger-full-access"`)
	s.Contains("default config.toml falls back to CLAUDE.md for project docs", image,
		"cat /opt/codex/defaults/config.toml", "project_doc_fallback_filenames")

	probe := func(desc, cmd, pattern string, env ...string) {
		s.Check(desc, func(t *testing.T) {
			out := codexRun(codexShort, image, cmd, nil, env...).Out
			codexExpect(t, codexGrep(out, pattern), "expected to match: "+pattern, out)
		})
	}
	probe("entrypoint seeds $CODEX_HOME/config.toml on first run",
		codexSeedProbe+`test -f "$HOME/.codex/config.toml" && echo OK`, `^OK$`)

	agents := []string{"adversarial-reviewer", "architect", "monorepo-coordinator", "security-reviewer", "spec-keeper"}
	s.Check("entrypoint composes every codex subagent", func(t *testing.T) {
		cmd := codexSeedProbe + `for a in ` + strings.Join(agents, " ") + `; do
     test -f "$HOME/.codex/agents/$a.toml" || echo "MISSING:$a"
   done`
		out := codexRun(codexShort, image, cmd, nil).Out
		codexExpect(t, !strings.Contains(out, "MISSING"), "subagent composition", out)
	})

	s.Check("advisors are read-only; spec-keeper is the single writer", func(t *testing.T) {
		cmd := codexSeedProbe + `for a in adversarial-reviewer architect monorepo-coordinator security-reviewer; do
     grep -q "sandbox_mode = \"read-only\"" "$HOME/.codex/agents/$a.toml" || echo "WRITABLE:$a"
   done
   grep -q "sandbox_mode = \"workspace-write\"" "$HOME/.codex/agents/spec-keeper.toml" || echo "NOT_WRITER:spec-keeper"`
		out := codexRun(codexShort, image, cmd, nil).Out
		codexExpect(t, !codexGrep(out, `WRITABLE|NOT_WRITER`), "subagent sandbox split", out)
	})

	probe("house rules install at $CODEX_HOME/AGENTS.md",
		codexSeedProbe+`test -s "$HOME/.codex/AGENTS.md" && echo OK`, `^OK$`)

	probe("CODEX_RESEED=1 overwrites an existing config", `
    mkdir -p "$HOME/.codex"
    echo "approval_policy = \"untrusted\"" > "$HOME/.codex/config.toml"
    /entrypoint.sh --version >/dev/null 2>&1
    grep -q "danger-full-access" "$HOME/.codex/config.toml" && echo RESEEDED || echo NOT_RESEEDED
  `, `^RESEEDED$`, "-e", "CODEX_RESEED=1")

	probe("entrypoint preserves an existing config.toml (no reseed)", `
    mkdir -p "$HOME/.codex"
    echo "model = \"USER_CUSTOM\"" > "$HOME/.codex/config.toml"
    /entrypoint.sh --version >/dev/null 2>&1
    grep -q "USER_CUSTOM" "$HOME/.codex/config.toml" && echo PRESERVED || echo CLOBBERED
  `, `^PRESERVED$`)

	probe("the workspace is left untouched", `
    /entrypoint.sh --version >/dev/null 2>&1
    found=$(ls -A /app 2>/dev/null | grep -v "^output$" || true)
    [ -z "$found" ] && echo UNTOUCHED || echo "MUTATED:$found"
  `, `^UNTOUCHED$`)

	// Phase 6: Direct LLM API
	if os.Getenv("OPENAI_API_KEY") == "" {
		s.Skip("live codex round-trip", "OPENAI_API_KEY not set")
		return
	}
	s.Check("live codex round-trip", func(t *testing.T) {
		out := codexRun(codexLive, image, `cd /app && codex exec --skip-git-repo-check \
       --dangerously-bypass-approvals-and-sandbox "Reply with exactly: PROVEO_OK"`, nil, "-e", "OPENAI_API_KEY").Out
		codexExpect(t, strings.Contains(out, "PROVEO_OK"), "live codex round-trip", out)
	})
}
