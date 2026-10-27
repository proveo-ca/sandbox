// SPEC: _spec/defs/browser-layer.puml
package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/imagebuild"
)

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

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

	floor := imagebuild.Specs["base-node-browser"].Floor
	for _, want := range []string{"command -v agent-browser", "AGENT_BROWSER_EXECUTABLE_PATH", "skill-data}/core/SKILL.md", "/opt/proveo/skills/agent-browser/SKILL.md"} {
		if !strings.Contains(floor, want) {
			t.Errorf("the base-node-browser floor probe must check %q — a stale :local image without it looks present", want)
		}
	}
}

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

func TestBrowserVariantsLayerOntoTheHarness(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ target, parent, user string }{
		{"opencode-browser", "opencode", "opencode"},
		{"codex-browser", "codex", "codex"},
		{"cursor-browser", "cursor", "cursor"},
		{"claudecode-browser", "claudecode", "claude"},
	} {
		s := imagebuild.Specs[tc.target]
		if s.Needs != tc.parent || s.Layer != tc.user {
			t.Errorf("%s must layer onto %s as user %s, got needs=%q user=%q", tc.target, tc.parent, tc.user, s.Needs, s.Layer)
		}
		if s.Parent != "" {
			t.Errorf("%s still builds the variant FROM a shared base (%s)", tc.target, s.Parent)
		}
		if s.Context != "defs/base-node-browser" {
			t.Errorf("%s must apply the base-node-browser layer, got context %q", tc.target, s.Context)
		}
	}
}

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
	if !strings.Contains(seedBody(t, lib), `proveo_seed_browser_skills "$target"`) {
		t.Error("proveo_seed must call proveo_seed_browser_skills so both backends seed the skill")
	}
	if !strings.Contains(lib, `command -v agent-browser >/dev/null 2>&1 || return 0`) {
		t.Error("the skill seed must be gated on the agent-browser binary, not the image name")
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

func TestBrowserVariantsAreDocumentedAsALayerOnTheirHarness(t *testing.T) {
	t.Parallel()
	readmes, err := filepath.Glob(filepath.Join(repoRoot(t), "defs", "*", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range imagebuild.Specs {
		if s.Dockerfile != "defs/base-node-browser/Dockerfile" || s.Needs == "" {
			continue
		}
		for _, path := range readmes {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(string(b), "\n") {
				if strings.Contains(line, "`proveo/"+name+"`") && strings.Contains(line, "`proveo/base-node-browser`") {
					t.Errorf("%s says %s builds from proveo/base-node-browser; it is the browser layer on proveo/%s:\n%s",
						filepath.Base(filepath.Dir(path))+"/README.md", name, s.Needs, line)
				}
			}
		}
	}
}
