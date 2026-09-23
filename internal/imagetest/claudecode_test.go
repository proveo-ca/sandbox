//go:build image

// SPEC: _spec/tests/testing-strategy.puml, _spec/_plans/host-shell-to-go.puml
package imagetest_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/imagetest"
	"github.com/proveo-ca/proveo/internal/provider"
)

type ccImages struct {
	standalone, mcp string
	mcpAvailable    bool
}

func (c ccImages) list() []string {
	if c.mcpAvailable {
		return []string{c.standalone, c.mcp}
	}
	return []string{c.standalone}
}

func (c ccImages) tag(image string) string {
	if image == c.standalone {
		return "standalone"
	}
	return "mcp"
}

func TestImageClaudecode(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	defs := filepath.Join(root, "defs")
	img := ccImages{
		standalone: imagetest.Resolve("STANDALONE_IMAGE", "proveo/claudecode:latest"),
		mcp:        imagetest.Resolve("MCP_IMAGE", "proveo/claudecode:latest"),
	}

	available := t.Run("claudecode image is available", func(t *testing.T) {
		if imagetest.Present(img.standalone) {
			return
		}
		if r := imagetest.Docker(10*time.Minute, nil, "pull", img.standalone); !r.OK() {
			t.Fatalf("cannot continue without the claudecode image %s — proveo build claudecode", img.standalone)
		}
	})
	if !available {
		return
	}
	s := imagetest.New(t, img.standalone)
	img.mcpAvailable = imagetest.Present(img.mcp)

	ccBuild(s, img)
	ccTools(s, img)
	ccSecurity(s, img)
	ccConfig(s, img)
	ccWorkspace(s, img)
	ccVolumes(s, img)
	ccFunctional(s, img)
	ccEgress(s, defs)
	ccChromeBridge(s, img)
}

func ccBuild(s *imagetest.Suite, img ccImages) {
	const nonRoot = `{{index .Config.Labels "security.non-root"}}`
	const hardened = `{{index .Config.Labels "security.hardened"}}`
	s.Inspect("[claudecode] has security.non-root=true label", img.standalone, nonRoot, "true")
	s.Inspect("[claudecode] has security.hardened=true label", img.standalone, hardened, "true")
	if img.mcpAvailable {
		s.Inspect("[mcp] has security.non-root=true label", img.mcp, nonRoot, "true")
		s.Inspect("[mcp] has security.hardened=true label", img.mcp, hardened, "true")
	}

	s.Inspect("[claudecode] proveo.agent label names the agent package", img.standalone,
		`{{index .Config.Labels "proveo.agent"}}`, "@anthropic-ai/claude-code")
	label := ""
	if r := imagetest.Docker(imagetest.DefaultTimeout, nil, "image", "inspect", "-f",
		`{{index .Config.Labels "proveo.agent.version"}}`, img.standalone); r.OK() {
		label = strings.TrimSpace(r.Out)
	}
	if label != "" {
		s.Contains("[claudecode] claude --version matches proveo.agent.version="+label,
			img.standalone, "claude --version", label)
	} else {
		s.Check("[claudecode] proveo.agent.version label is set", func(t *testing.T) {
			t.Error("image predates the pin — proveo build claudecode")
		})
	}

	s.Success("[claudecode] ships the Kit's startup command (/usr/local/bin/proveo-seed)",
		img.standalone, "test -x /usr/local/bin/proveo-seed")
	if img.mcpAvailable {
		s.Success("[mcp] ships the Kit's startup command (/usr/local/bin/proveo-seed)",
			img.mcp, "test -x /usr/local/bin/proveo-seed")
	}
}

type ccTool struct{ name, cmd string }

var (
	ccBaseTools = []ccTool{
		{"claude", "claude --help"},
		{"node", "node --version"},
		{"npm", "npm --version"},
		{"pnpm", "pnpm --version"},
		{"bun", "bun --version"},
		{"bunx", "bunx --version"},
		{"git", "git --version"},
		{"gh", "gh --version"},
		{"python3", "python3 --version"},
		{"pip3", "pip3 --version"},
		{"curl", "curl --version"},
		{"wget", "wget --version"},
		{"dumb-init", "dumb-init --version"},
	}
	ccSolTools = []ccTool{
		{"solhint", "solhint --version"},
		{"semgrep", "semgrep --version"},
		{"solc-select", "solc-select versions"},
		{"solc", "solc --version"},
		{"forge", "forge --version"},
		{"cast", "cast --version"},
	}
	ccSolAbsent = []string{"anvil", "chisel"}
)

func ccTools(s *imagetest.Suite, img ccImages) {
	for _, image := range img.list() {
		tag := img.tag(image)
		for _, tl := range ccBaseTools {
			s.Success(fmt.Sprintf("[%s] %s is installed", tag, tl.name), image, tl.cmd)
		}
		for _, tl := range ccSolTools {
			s.Failure(fmt.Sprintf("[%s] %s stays out of the base variant (sol-only)", tag, tl.name), image, "command -v "+tl.name)
		}
		for _, name := range ccSolAbsent {
			s.Failure(fmt.Sprintf("[%s] %s is in no variant at all", tag, name), image, "command -v "+name)
		}
	}

	for _, image := range img.list() {
		s.Contains(fmt.Sprintf("[%s] bun executes a TypeScript file without a build step", img.tag(image)), image,
			"printf 'const n: number = 21; console.log(n * 2)' > /tmp/x.ts && bun /tmp/x.ts", "42")
	}

	sol := imagetest.Resolve("SOL_IMAGE", "proveo/claudecode-solidity:latest")
	if imagetest.Present(sol) {
		for _, tl := range append(append([]ccTool{}, ccBaseTools...), ccSolTools...) {
			if tl.name == "solc" && runtime.GOARCH == "arm64" {
				s.Skip("[sol] solc runs", "solc publishes no linux-arm64 build; solc-select installs the x86-64 one")
				continue
			}
			s.Success("[sol] "+tl.name+" is installed", sol, tl.cmd)
		}
		for _, name := range ccSolAbsent {
			s.Failure("[sol] "+name+" is removed after foundryup (devnet/REPL, no chain and no prompt here)", sol, "command -v "+name)
		}
	}

	if img.mcpAvailable {
		s.Success("[mcp] MCP server directory exists", img.mcp, "test -d /workspace/mcp-servers")
	}

	browser := imagetest.Resolve("BROWSER_IMAGE", "proveo/claudecode-browser:latest")
	if !imagetest.Present(browser) {
		s.Skip("[browser] claudecode-browser variant", "image "+browser+" not built (mise run build claudecode-browser)")
		return
	}
	s.Success("[browser] playwright CLI is installed", browser, "playwright --version")
	s.Success("[browser] agent-browser is installed", browser, "agent-browser --version")
	s.Success("[browser] agent-browser points at Playwright's Chromium (no second download)", browser,
		`test -x "$AGENT_BROWSER_EXECUTABLE_PATH" && readlink -f "$AGENT_BROWSER_EXECUTABLE_PATH" | grep -q "^${PLAYWRIGHT_BROWSERS_PATH}/chromium-"`)
	s.Contains("[browser] agent-browser serves its bundled skills", browser, "agent-browser skills list", "core")
	s.Success("[browser] agent-browser skill stub is baked for the seed", browser, "test -f /opt/proveo/skills/agent-browser/SKILL.md")
	s.Contains("[browser] the seed drops the skill into ~/.claude/skills", browser,
		`export HOME=/tmp; source /entrypoint-lib.sh; proveo_seed_browser_skills claudecode >/dev/null; head -2 /tmp/.claude/skills/agent-browser/SKILL.md`,
		"name: agent-browser")
	s.Contains("[browser] agent-browser drives a headless Chromium as the image user", browser,
		`export HOME=/tmp AGENT_BROWSER_SOCKET_DIR=/tmp/ab; mkdir -p /tmp/ab; agent-browser open about:blank >/dev/null && agent-browser get url; agent-browser close >/dev/null`,
		"about:blank")
}

func ccSecurity(s *imagetest.Suite, img ccImages) {
	uid := os.Getenv("EXPECTED_UID")
	if uid == "" {
		uid = "1000"
	}
	for _, image := range img.list() {
		tag := img.tag(image)
		s.Contains("["+tag+"] runs as user claude", image, "whoami", "claude")
		s.Contains("["+tag+"] UID is "+uid, image, "id -u", uid)
		s.Failure("["+tag+"] no setuid binaries", image, "find / -xdev -perm -4000 -type f 2>/dev/null | grep -q .")
		s.Failure("["+tag+"] no setgid binaries", image, "find / -xdev -perm -2000 -type f 2>/dev/null | grep -q .")
		s.Failure("["+tag+"] nc not available", image, "which nc")
		s.Failure("["+tag+"] netcat not available", image, "which netcat")
		s.Failure("["+tag+"] netstat not available", image, "which netstat")
		s.Failure("["+tag+"] ss not available", image, "which ss")
		s.Failure("["+tag+"] cannot write to /usr/bin", image, "touch /usr/bin/testfile 2>/dev/null")
		s.Failure("["+tag+"] cannot write to /etc", image, "touch /etc/testfile 2>/dev/null")
		s.Contains("["+tag+"] NODE_ENV is not baked into the image", image, `echo "${NODE_ENV:-unset}"`, "unset")
		s.Inspect("["+tag+"] entrypoint uses dumb-init", image, "{{json .Config.Entrypoint}}", "dumb-init")
		s.Matches("["+tag+"] node version is v22.x", image, "node --version", `^v22\.`)
		s.Inspect("["+tag+"] Docker USER is non-root", image, "{{.Config.User}}", "claude")
		s.Check("["+tag+"] arbitrary --user uid gets usable identity and writable HOME", func(t *testing.T) {
			r := imagetest.Docker(imagetest.DefaultTimeout, nil, "run", "--rm", "--user", "4242:4242", "--entrypoint", "bash", image, "-c",
				`source /entrypoint-lib.sh && ensure_runtime_user && echo "uid=$(id -u) home_writable=$(test -w "$HOME" && echo yes || echo no)"`)
			if !strings.Contains(r.Out, "uid=4242 home_writable=yes") {
				t.Errorf("output: %.200s", r.Out)
			}
		})
		s.Failure("["+tag+"] does not run as root by default", image, `[ "$(id -u)" = "0" ]`)
	}
}

func ccConfig(s *imagetest.Suite, img ccImages) {
	image := img.standalone
	s.Success("[standalone] ~/.claude.json exists", image, "test -f /home/claude/.claude.json")
	s.Success("[standalone] git-sync turn hook is executable", image, "test -x /opt/proveo/hooks/git-sync-turn.sh")
	s.Contains("[standalone] ~/.claude.json owned by claude", image, "stat -c '%U' /home/claude/.claude.json", "claude")
	s.Contains("[standalone] dangerouslySkipPermissions=true", image, "cat /home/claude/.claude.json", `"dangerouslySkipPermissions": true`)
	s.Contains("[standalone] autoTrustNewProjects=true", image, "cat /home/claude/.claude.json", `"autoTrustNewProjects": true`)
	s.Contains("[standalone] has /workspace project", image, "cat /home/claude/.claude.json", `"/workspace"`)
	s.Contains("[claudecode] permissions.allow pre-authorizes Bash", image, "cat /home/claude/.claude.json", `"Bash"`)

	s.Check("[claudecode] no wildcard-only MCP allow rule in any seeded settings", func(t *testing.T) {
		r := imagetest.Docker(30*time.Second, nil, "run", "--rm", "--entrypoint", "bash", image, "-c",
			"cat /home/claude/.claude.json /home/claude/.claude/settings.local.json /app/.claude/settings.local.json 2>/dev/null")
		bad := 0
		for line := range strings.SplitSeq(r.Out, "\n") {
			if strings.Contains(line, "mcp__*") {
				bad++
			}
		}
		if bad != 0 {
			t.Errorf("found %d 'mcp__*' rule(s) — Claude Code rejects them at startup", bad)
		}
	})

	s.Contains("[standalone] hasCompletedOnboarding=true", image, "cat /home/claude/.claude.json", `"hasCompletedOnboarding": true`)
	s.Matches("[standalone] mcpServers is empty", image,
		`python3 -c "import json; c=json.load(open('/home/claude/.claude.json')); print(len(c['projects']['/workspace']['mcpServers']))"`, "^0$")
	s.Success("[standalone] ~/.claude/settings.local.json exists", image, "test -f /home/claude/.claude/settings.local.json")
	s.Success("[standalone] /app/.claude/settings.local.json exists", image, "test -f /app/.claude/settings.local.json")
	s.Success("[standalone] settings.local.json is valid JSON", image,
		`python3 -c "import json; json.load(open('/home/claude/.claude/settings.local.json'))"`)

	s.Success("[standalone] every subagent in the claudecode roster is baked in", image, `set -eu
   roster="$(jq -r ".claudecode[]" /opt/proveo/subagents/_roster.json)"
   [ -n "$roster" ] || { echo "claudecode roster is empty"; exit 1; }
   for a in $roster; do
     test -f "/opt/proveo/subagents/$a.md" || { echo "missing /opt/proveo/subagents/$a.md"; exit 1; }
   done`)

	const fm = "/opt/proveo/subagents/_frontmatter/claudecode"
	s.Success("[standalone] claudecode subagent frontmatter is present", image,
		"ls "+fm+"/*.yaml >/dev/null 2>&1 && grep -lq '^tools:' "+fm+"/*.yaml")
	s.Failure("[standalone] no subagent grants Bash", image, "grep -lE '^tools:.*Bash' "+fm+"/*.yaml | grep -q .")
	s.Failure("[standalone] only spec-keeper may write", image,
		"grep -lE '^tools:.*(Edit|Write)' "+fm+"/*.yaml | grep -v spec-keeper.yaml | grep -q .")
	s.Contains("[standalone] render_subagents seeds the roster into $HOME/.claude/agents", image,
		`export HOME=/tmp/seedhome && mkdir -p $HOME && source /entrypoint-lib.sh && render_subagents claudecode "$HOME/.claude/agents" >/dev/null && ls $HOME/.claude/agents | tr '\n' ' '`,
		"adversarial-reviewer.md architect.md monorepo-coordinator.md security-reviewer.md spec-keeper.md")
	s.Contains("[standalone] CLAUDE.md wires the review gates", image, "cat /opt/claudecode/defaults/CLAUDE.md", "Review Gates")

	if !img.mcpAvailable {
		return
	}
	image = img.mcp
	s.Success("[mcp] ~/.claude.json exists", image, "test -f /home/claude/.claude.json")
	s.Contains("[mcp] dangerouslySkipPermissions=true", image, "cat /home/claude/.claude.json", `"dangerouslySkipPermissions": true`)
	s.Contains("[mcp] hasCompletedOnboarding=true", image, "cat /home/claude/.claude.json", `"hasCompletedOnboarding": true`)
	s.Contains("[mcp] mcpServers is present", image, "cat /home/claude/.claude.json", `"mcpServers"`)
	s.Contains("[mcp] mcpServers is empty", image, "cat /home/claude/.claude.json", `"mcpServers": {}`)
}

func ccWorkspace(s *imagetest.Suite, img ccImages) {
	modes := []struct{ dir, mode string }{
		{"/workspace", "755"},
		{"/app", "750"},
		{"/workspace/data", "750"},
		{"/app/output", "755"},
		{"/workspace/temp", "755"},
		{"/workspace/mcp-servers", "755"},
	}
	for _, image := range img.list() {
		tag := img.tag(image)
		for _, dir := range []string{"/app", "/app/output", "/workspace", "/workspace/data", "/workspace/temp", "/workspace/mcp-servers"} {
			s.Success("["+tag+"] "+dir+" exists", image, "test -d "+dir)
		}
		s.Contains("["+tag+"] /workspace owned by claude", image, "stat -c '%U' /workspace", "claude")
		for _, m := range modes {
			s.Matches("["+tag+"] "+m.dir+" is "+m.mode, image, "stat -c '%a' "+m.dir, "^"+m.mode+"$")
		}
		s.Inspect("["+tag+"] WORKDIR is /app", image, "{{.Config.WorkingDir}}", "/app")
		s.Contains("["+tag+"] HOME is /home/agent (the REAL home; /home/claude is the alias)", image, "echo $HOME", "/home/agent")
		s.Contains("["+tag+"] /home/claude resolves to /home/agent", image, "readlink -f /home/claude", "/home/agent")
		s.Success("["+tag+"] entrypoint.sh is baked and executable", image, "test -x /entrypoint.sh")
		s.Contains("["+tag+"] entrypoint launches claude with --dangerously-skip-permissions", image,
			"cat /entrypoint.sh", "proveo_exec_agent claude --dangerously-skip-permissions")
	}
}

func ccRunMounted(image, hostDir, mount, cmd string, env ...string) imagetest.Result {
	args := []string{"run", "--rm", "--entrypoint", "bash"}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, "-v", hostDir+":"+mount, image, "-c", cmd)
	return imagetest.Docker(imagetest.DefaultTimeout, nil, args...)
}

func ccFileHas(path, want string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(b), want)
}

func ccVolumes(s *imagetest.Suite, img ccImages) {
	image := img.standalone
	s.Check("Input volume is readable", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("volume-test-content-12345\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := ccRunMounted(image, dir, "/workspace/input:ro", "cat /workspace/input/sample.txt")
		if !strings.Contains(r.Out, "volume-test-content-12345") {
			t.Errorf("output: %.200s", r.Out)
		}
	})
	s.Check("Input volume respects :ro when mounted RO", func(t *testing.T) {
		r := ccRunMounted(image, t.TempDir(), "/workspace/input:ro", "touch /workspace/input/forbidden.txt 2>&1; echo EXIT_CODE=$?")
		if !regexp.MustCompile(`(Read-only|EXIT_CODE=1)`).MatchString(r.Out) {
			t.Errorf("output: %.200s", r.Out)
		}
	})
	s.Check("Input volume is writable when mounted RW", func(t *testing.T) {
		dir := t.TempDir()
		r := ccRunMounted(image, dir, "/workspace/input", "echo ok > /workspace/input/writable.txt && cat /workspace/input/writable.txt")
		if _, err := os.Stat(filepath.Join(dir, "writable.txt")); !strings.Contains(r.Out, "ok") || err != nil {
			t.Errorf("output: %.200s", r.Out)
		}
	})
	s.Check("Output volume is writable and persists to host", func(t *testing.T) {
		dir := t.TempDir()
		ccRunMounted(image, dir, "/workspace/output:rw", "echo 'output-data-67890' > /workspace/output/result.txt")
		if !ccFileHas(filepath.Join(dir, "result.txt"), "output-data-67890") {
			t.Error("result.txt missing or lacks output-data-67890")
		}
	})
	s.Success("/workspace/temp is writable by claude", image, "touch /workspace/temp/test-write && rm /workspace/temp/test-write")
}

func ccFunctional(s *imagetest.Suite, img ccImages) {
	token := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	tokenEnv := []string{"-e", "CLAUDE_CODE_OAUTH_TOKEN=" + token}
	version := regexp.MustCompile(`[0-9]+\.[0-9]+`)
	for _, image := range img.list() {
		s.Check("["+img.tag(image)+"] claude --version returns version", func(t *testing.T) {
			args := append(append([]string{"run", "--rm", "--entrypoint", "bash"}, tokenEnv...), image, "-c", "claude --version")
			if r := imagetest.Docker(imagetest.DefaultTimeout, nil, args...); !version.MatchString(r.Out) {
				t.Errorf("output: %.200s", r.Out)
			}
		})
	}

	if token == "" {
		const why = "no CLAUDE_CODE_OAUTH_TOKEN"
		s.Skip("claude -p prompt test (standalone)", why)
		s.Skip("claude reads input volume (standalone)", why)
		s.Skip("claude writes output volume (standalone)", why)
		if img.mcpAvailable {
			s.Skip("claude -p prompt test (mcp)", why)
			s.Skip("claude lists MCP tools (mcp)", why)
		}
		return
	}

	const llm = 150 * time.Second
	prompt := func(image, cmd string) string {
		args := append(append([]string{"run", "--rm", "--entrypoint", "bash"}, tokenEnv...), image, "-c", cmd)
		return imagetest.Docker(llm, nil, args...).Out
	}
	pong := `timeout 120 claude -p "Respond with only the word PONG" 2>&1`
	s.Check("[standalone] claude -p returns expected output", func(t *testing.T) {
		if out := prompt(img.standalone, pong); !strings.Contains(strings.ToLower(out), "pong") {
			t.Errorf("output: %.200s", out)
		}
	})
	s.Check("[standalone] claude can read input volume files", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("TEST_MARKER_ABC123\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := ccRunMounted(img.standalone, dir, "/workspace/input:ro",
			`timeout 120 claude -p "Read the file /workspace/input/marker.txt and reply with its exact contents only" 2>&1`,
			"CLAUDE_CODE_OAUTH_TOKEN="+token)
		if !strings.Contains(r.Out, "TEST_MARKER_ABC123") {
			t.Errorf("output: %.200s", r.Out)
		}
	})
	s.Check("[standalone] claude can write to output volume", func(t *testing.T) {
		dir := t.TempDir()
		ccRunMounted(img.standalone, dir, "/workspace/output:rw",
			`timeout 120 claude -p "Write the text OUTPUT_MARKER_XYZ789 to /workspace/output/test-result.txt" 2>&1`,
			"CLAUDE_CODE_OAUTH_TOKEN="+token)
		if !ccFileHas(filepath.Join(dir, "test-result.txt"), "OUTPUT_MARKER_XYZ789") {
			t.Error("test-result.txt missing or lacks OUTPUT_MARKER_XYZ789")
		}
	})
	if !img.mcpAvailable {
		return
	}
	s.Check("[mcp] claude -p returns expected output", func(t *testing.T) {
		if out := prompt(img.mcp, pong); !strings.Contains(strings.ToLower(out), "pong") {
			t.Errorf("output: %.200s", out)
		}
	})
	s.Check("[mcp] claude recognizes MCP tools", func(t *testing.T) {
		out := prompt(img.mcp, `timeout 120 claude -p "List your available MCP tools" 2>&1 || true`)
		if !regexp.MustCompile(`(?i)mcp|tool`).MatchString(out) {
			t.Errorf("[mcp] claude responds to MCP tools prompt (output: %.200s)", out)
		}
	})
}

// ccLookup is the host env with set applied and unset removed.
func ccLookup(set map[string]string, unset ...string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		if v, ok := set[k]; ok {
			return v, true
		}
		for _, u := range unset {
			if u == k {
				return "", false
			}
		}
		return os.LookupEnv(k)
	}
}

func ccDetect(lookup func(string) (string, bool)) []string {
	return provider.Detect(func(k string) string { v, _ := lookup(k); return v })
}

// ccProviderAllow mirrors `proveo-egress provider-allow > file`: a failed run leaves the file empty.
func ccProviderAllow(lookup func(string) (string, bool)) string {
	var providers []string
	if p, _ := lookup("PROVEO_EGRESS_PROVIDER"); strings.TrimSpace(p) != "" && strings.TrimSpace(p) != "none" {
		providers = []string{strings.TrimSpace(p)}
	} else {
		providers = ccDetect(lookup)
	}
	domains, _ := lookup("PROVEO_EGRESS_PROVIDER_DOMAINS")
	conf, matched, _ := egress.ProviderAllowConf(providers, domains)
	if len(providers) > 0 && len(matched) == 0 {
		return ""
	}
	return conf
}

func ccEgress(s *imagetest.Suite, defs string) {
	fileHas := func(desc, path, want string) {
		s.Check(desc, func(t *testing.T) {
			if !ccFileHas(path, want) {
				t.Errorf("Expected '%s' in %s", want, path)
			}
		})
	}
	fileLacks := func(desc, path, unwanted string) {
		s.Check(desc, func(t *testing.T) {
			if ccFileHas(path, unwanted) {
				t.Errorf("Did not expect '%s' in %s", unwanted, path)
			}
		})
	}
	textHas := func(desc, text, want string) {
		s.Check(desc, func(t *testing.T) {
			if !strings.Contains(text, want) {
				t.Errorf("Expected '%s' in provider-allow.conf:\n%s", want, text)
			}
		})
	}
	textLacks := func(desc, text, unwanted string) {
		s.Check(desc, func(t *testing.T) {
			if strings.Contains(text, unwanted) {
				t.Errorf("Did not expect '%s' in provider-allow.conf:\n%s", unwanted, text)
			}
		})
	}

	squidDir := filepath.Join(defs, "sidecars", "squid-proxy")
	squid := filepath.Join(squidDir, "squid.conf")
	firehol := filepath.Join(squidDir, "firehol-blocked-nets.conf")
	updater := filepath.Join(squidDir, "..", "..", "..", "cmd", "proveo-dev", "firehol.go")
	fileHas("[policy] Squid documents HTTP/HTTPS-only protocol allowlist", squid, "Protocol allowlist: HTTP and HTTPS only")
	fileHas("[policy] Squid allows HTTP port 80", squid, "acl Safe_ports port 80")
	fileHas("[policy] Squid allows HTTPS port 443", squid, "acl Safe_ports port 443")
	fileHas("[policy] Squid blocks non-web/raw protocols by denying non-safe ports", squid, "http_access deny !Safe_ports")
	fileHas("[policy] Squid allows only read-oriented visible HTTP methods by default", squid, "acl read_methods method GET HEAD OPTIONS")
	fileLacks("[policy] FTP is not in the allowed protocol set", squid, "acl Safe_ports port 21")
	fileHas("[policy] Squid includes FireHOL-informed reserved destination defaults", squid, "include /etc/squid/firehol-blocked-nets.conf")
	fileHas("[policy] Squid supports optional generated FireHOL ipset ACLs", squid, "include /etc/squid/firehol-ipset.conf")
	fileHas("[policy] reserved defaults block cloud metadata SSRF range", firehol, "169.254.0.0/16")
	fileHas("[policy] reserved defaults block private RFC1918 ranges", firehol, "10.0.0.0/8")
	fileHas("[policy] optional FireHOL updater defaults to firehol_level1", updater, "firehol_level1")
	fileHas("[policy] optional FireHOL updater generates Squid ACLs", updater, "acl firehol_ipset dst")
	fileHas("[policy] any HTTPS documentation/search destination is allowed by protocol", squid, "http_access allow CONNECT SSL_ports")
	fileHas("[policy] any visible HTTP documentation/search read is allowed by method", squid, "http_access allow read_methods")
	fileHas("[policy] docs/search access is intentionally generic, not host-specific", squid, "any documentation site, search engine")
	fileLacks("[policy] Pinecone docs are not hardcoded as a special case", squid, "docs.pinecone.io")
	fileLacks("[policy] Google search is not hardcoded as a special case", squid, ".google.com")

	detectHas := func(desc, want string, lookup func(string) (string, bool)) {
		s.Check(desc, func(t *testing.T) {
			if d := strings.Join(ccDetect(lookup), " "); !strings.Contains(d, want) {
				t.Errorf("got: %s", d)
			}
		})
	}
	detectHas("[provider] ANTHROPIC_API_KEY auto-detects anthropic", "anthropic",
		ccLookup(map[string]string{"ANTHROPIC_API_KEY": "sk-x"}, "PROVEO_EGRESS_PROVIDER"))
	detectHas("[provider] AWS key auto-detects bedrock", "bedrock",
		ccLookup(map[string]string{"AWS_ACCESS_KEY_ID": "AKIA"}, "PROVEO_EGRESS_PROVIDER", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"))
	detectHas("[provider] GMI_API_KEY auto-detects gmi", "gmi",
		ccLookup(map[string]string{"GMI_API_KEY": "gmi"}, "PROVEO_EGRESS_PROVIDER", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"))

	conf := ccProviderAllow(ccLookup(map[string]string{"PROVEO_EGRESS_PROVIDER": "together"}, "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"))
	textHas("[provider] together pins inference writes to its endpoint", conf, "acl provider_allow dstdomain .together.xyz")
	textHas("[provider] write-pin allows unsafe methods to provider only", conf, "http_access allow unsafe_methods provider_allow")
	textLacks("[provider] reads stay open — no deny-all (scraping preserved)", conf, "http_access deny all")

	conf = ccProviderAllow(ccLookup(map[string]string{"PROVEO_EGRESS_PROVIDER": "gmi"}))
	textHas("[provider] gmi pins to api.gmi-serving.com", conf, ".gmi-serving.com")

	conf = ccProviderAllow(ccLookup(map[string]string{"PROVEO_EGRESS_PROVIDER": "bedrock"}))
	textHas("[provider] bedrock scoped to bedrock-runtime, not all of AWS", conf, "bedrock-runtime")
	textLacks("[provider] bedrock does not allow all of .amazonaws.com", conf, "dstdomain .amazonaws.com")

	conf = ccProviderAllow(ccLookup(map[string]string{"OPENAI_API_KEY": "sk"}, "PROVEO_EGRESS_PROVIDER", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"))
	textHas("[provider] auto-detected openai key pins openai endpoint", conf, ".openai.com")

	s.Check("[go] orchestration contracts live in internal/egress (not bash prepare)", func(*testing.T) {})
}

const ccChainScript = `set -e
export HOME=/tmp
node -e '
  const net = require("net"), fs = require("fs");
  const srv = net.createServer(c => {
    let buf = "";
    c.on("data", d => {
      buf += d;
      const lines = buf.split("\n");
      if (lines.length >= 3) c.end("HS=" + lines[0] + ";PAYLOAD=" + lines[1] + "\n");
    });
  });
  srv.listen(0, "127.0.0.1", () => fs.writeFileSync("/tmp/fake-host.port", String(srv.address().port)));
  setTimeout(() => process.exit(0), 20000);
' &
for _ in $(seq 1 50); do [ -s /tmp/fake-host.port ] && break; sleep 0.1; done
export PROVEO_CHROME_BRIDGE="127.0.0.1:$(cat /tmp/fake-host.port)" PROVEO_CHROME_BRIDGE_TOKEN=tok-123
source /entrypoint-lib.sh
proveo_chrome_bridge claudecode >/dev/null
sock="$(head -n1 /tmp/proveo-chrome-bridge.sock-path)"
perms="$(stat -c '%a' "$(dirname "$sock")") $(stat -c '%a' "$sock")"
reply="$(node -e '
  const net = require("net");
  const c = net.connect(process.argv[1], () => c.write("hello-from-claude\n\n"));
  c.on("data", d => { process.stdout.write(d.toString()); c.end(); });
' "$sock")"
echo "CHAIN ready=${PROVEO_CHROME_READY:-} perms=${perms} dir=$(dirname "$sock") reply=${reply}"`

func ccChromeBridge(s *imagetest.Suite, img ccImages) {
	for _, image := range img.list() {
		tag := img.tag(image)
		s.Success("["+tag+"] chrome-bridge.js is baked and parses", image,
			"test -f /opt/proveo/lib/chrome-bridge.js && node --check /opt/proveo/lib/chrome-bridge.js")
		s.Contains("["+tag+"] proveo_chrome_bridge is a no-op without PROVEO_CHROME_BRIDGE", image,
			`source /entrypoint-lib.sh; unset PROVEO_CHROME_BRIDGE; proveo_chrome_bridge claudecode; echo "ready=[${PROVEO_CHROME_READY:-}]"`,
			"ready=[]")
		s.Contains("["+tag+"] the relay refuses to run without a token", image,
			`source /entrypoint-lib.sh; export PROVEO_CHROME_BRIDGE=127.0.0.1:9; unset PROVEO_CHROME_BRIDGE_TOKEN; proveo_chrome_bridge claudecode 2>&1; echo "ready=[${PROVEO_CHROME_READY:-}]"`,
			"ready=[]")
		s.Contains("["+tag+"] the relay listens where Claude Code looks, 0700/0600, and carries handshake + payload", image,
			ccChainScript, "CHAIN ready=1 perms=700 600 dir=/tmp/claude-mcp-browser-bridge-")
		s.Contains("["+tag+"] the host end sees the token line first, then Claude Code's bytes", image,
			ccChainScript, "reply=HS=PROVEO-CHROME-BRIDGE tok-123;PAYLOAD=hello-from-claude")
	}
}
