// SPEC: _spec/defs/browser-layer.puml, _spec/defs/claudecode/chrome-bridge.puml
package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/chromebridge"
)

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// The browser layer pins agent-browser the way it pins Playwright: by version AND
// by digest of the tarball it unpacks, and it points the binary at the Chromium
// Playwright already installed rather than letting `agent-browser install` fetch a
// second one. A layer that drifts on any of these rebuilds into something the
// floor probe and the seed no longer describe.
func TestBrowserLayerPinsAgentBrowserAndReusesPlaywrightsChromium(t *testing.T) {
	t.Parallel()
	df := readRepoFile(t, "defs/base-node-browser/Dockerfile")
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)^ARG AGENT_BROWSER_VERSION=\d+\.\d+\.\d+$`),
		regexp.MustCompile(`(?m)^ARG AGENT_BROWSER_SHA256=[0-9a-f]{64}$`),
		regexp.MustCompile(`sha256sum -c`),
		regexp.MustCompile(`AGENT_BROWSER_EXECUTABLE_PATH=/opt/proveo/chromium/chrome`),
		regexp.MustCompile(`AGENT_BROWSER_SKILLS_DIR=/opt/agent-browser/skill-data`),
		regexp.MustCompile(`COPY skills/ /opt/proveo/skills/`),
	} {
		if !want.MatchString(df) {
			t.Errorf("defs/base-node-browser/Dockerfile lacks %s", want)
		}
	}
	if strings.Contains(df, "agent-browser install") && !strings.Contains(df, "would download a SECOND Chromium") {
		t.Error("the Dockerfile must not run `agent-browser install`; Playwright's Chromium is the browser")
	}
	if strings.Contains(df, "npm install -g agent-browser") {
		t.Error("agent-browser comes from the pinned tarball, not `npm install -g` (engines.node >= 24 on a Node 22 floor)")
	}

	ensure := readRepoFile(t, "defs/base-node-browser/ensure.sh")
	for _, want := range []string{"command -v agent-browser", "AGENT_BROWSER_EXECUTABLE_PATH", "skill-data}/core/SKILL.md", "/opt/proveo/skills/agent-browser/SKILL.md"} {
		if !strings.Contains(ensure, want) {
			t.Errorf("ensure.sh's floor probe must check %q — a stale :local image without it looks present", want)
		}
	}
}

// The seeded skill is what makes the tool discoverable. Its frontmatter has to
// parse for every harness that reads it (name + description are the only fields
// all three recognise), and it must not tell the agent to install anything.
func TestBrowserSkillStubIsHarnessNeutralAndInstallFree(t *testing.T) {
	t.Parallel()
	skill := readRepoFile(t, "defs/base-node-browser/skills/agent-browser/SKILL.md")
	if !strings.HasPrefix(skill, "---\nname: agent-browser\ndescription: ") {
		t.Fatalf("SKILL.md must open with name then description frontmatter, got %q", firstLines(skill, 3))
	}
	for _, banned := range []string{"npm i -g agent-browser\n", "run `agent-browser install`", "hidden: true"} {
		if strings.Contains(skill, banned) && !strings.Contains(skill, "Do not run") {
			t.Errorf("SKILL.md must not instruct %q — the sandbox is already provisioned", banned)
		}
	}
	if !strings.Contains(skill, "agent-browser skills get core") {
		t.Error("SKILL.md must point at `agent-browser skills get core`, the version-matched guide")
	}
}

// One seed function writes the skill into each harness's USER-level skills dir.
// The table is spelled out per target, cecli included, so a new harness has to
// take a position rather than inherit silence.
func TestBrowserSkillSeedNamesEveryHarnessSkillsDir(t *testing.T) {
	t.Parallel()
	lib := readRepoFile(t, "packages/lib/entrypoint-lib.sh")
	arms := dashedEchoArms(t, lib, "_browser_skill_dir")
	want := map[string]string{
		"claudecode": ".claude/skills",
		"cursor":     ".cursor/skills",
		"opencode":   ".config/opencode/skills",
	}
	for target, dir := range want {
		got := arms[target]
		if len(got) != 1 || got[0] != dir {
			t.Errorf("_browser_skill_dir %s = %v, want %q", target, got, dir)
		}
	}
	if !strings.Contains(caseBody(t, lib, "_browser_skill_dir"), "cecli") {
		t.Error("_browser_skill_dir must state cecli's (empty) case explicitly")
	}
	if !strings.Contains(seedBody(t, lib), `proveo_seed_browser_skills "$target"`) {
		t.Error("proveo_seed must call proveo_seed_browser_skills so both backends seed the skill")
	}
	if !strings.Contains(lib, `command -v agent-browser >/dev/null 2>&1 || return 0`) {
		t.Error("the skill seed must be gated on the agent-browser binary, not the image name")
	}
}

// The two halves of the Claude in Chrome bridge share three literals: the socket
// directory prefix Claude Code uses, the handshake line, and the env var names.
// They live in two languages, so the contract is checked textually.
func TestChromeBridgeHalvesAgreeOnTheWireContract(t *testing.T) {
	t.Parallel()
	js := readRepoFile(t, "defs/claudecode/mcp/proveo-lib/chrome-bridge.js")
	for _, want := range []string{
		`const HANDSHAKE_PREFIX = "PROVEO-CHROME-BRIDGE ";`,
		`const SOCKET_DIR_PREFIX = "` + chromebridge.SocketDirPrefix + `";`,
		`process.env.` + chromebridge.EnvAddr,
		`process.env.` + chromebridge.EnvToken,
		`fs.chmodSync(dir, 0o700)`,
		`fs.chmodSync(sock, 0o600)`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("chrome-bridge.js lacks %q", want)
		}
	}
	if hs := chromebridge.Handshake("T"); hs != "PROVEO-CHROME-BRIDGE T\n" {
		t.Errorf("Go handshake = %q", hs)
	}

	lib := readRepoFile(t, "packages/lib/entrypoint-lib.sh")
	for _, want := range []string{
		`[[ -n "${PROVEO_CHROME_BRIDGE:-}" ]] || return 0`,
		`PROVEO_CHROME_BRIDGE_TOKEN`,
		`readonly PROVEO_CHROME_BRIDGE_JS=/opt/proveo/lib/chrome-bridge.js`,
		`hasCompletedClaudeInChromeOnboarding`,
	} {
		if !strings.Contains(lib, want) {
			t.Errorf("entrypoint-lib.sh proveo_chrome_bridge lacks %q", want)
		}
	}
	if strings.Contains(lib, `j.claudeInChromeDefaultEnabled = true`) {
		t.Error("claudeInChromeDefaultEnabled must not be persisted into the operator's ~/.claude.json (see proveo_chrome_bridge)")
	}
	ep := readRepoFile(t, "defs/claudecode/mcp/entrypoint.sh")
	for _, want := range []string{"proveo_chrome_bridge claudecode", `CLAUDE_CHROME_ARGS=(--chrome)`, `"${CLAUDE_CHROME_ARGS[@]}"`} {
		if !strings.Contains(ep, want) {
			t.Errorf("claudecode entrypoint lacks %q", want)
		}
	}
	man := readRepoFile(t, "defs/claudecode/harness.manifest")
	if !strings.Contains(man, "hostBrowser: claude-in-chrome") {
		t.Error("claudecode's manifest must declare hostBrowser: claude-in-chrome — it is what offers the add-on")
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

var seedFnRe = regexp.MustCompile(`(?s)\nproveo_seed\(\) \{\n(.*?)\n\}\n`)

func seedBody(t *testing.T, lib string) string {
	t.Helper()
	m := seedFnRe.FindStringSubmatch(lib)
	if m == nil {
		t.Fatal("proveo_seed() not found in entrypoint-lib.sh")
	}
	return m[1]
}
