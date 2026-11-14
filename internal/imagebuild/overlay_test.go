// SPEC: _spec/_devops/agent-update-prompt.puml
package imagebuild

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestNewerOrdersReleasesAndCountsUnorderableOnesAsAhead(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		eco, a, b string
		want      bool
	}{
		{"npm", "2.1.322", "2.1.312", true},
		{"npm", "2.1.312", "2.1.322", false},
		{"npm", "2.1.322", "2.1.322", false},
		{"npm", "2.10.0", "2.9.9", true},
		{"npm", "0.167.2", "0.99.0", true},
		{"npm", "3.0", "2.9.9", true},
		{"npm", "2.1.322", "", true},
		{"npm", "", "2.1.322", false},
		{"npm", "2.1.322-beta.1", "2.1.322", true},
		{"cursor", "2026.11.11-b912975", "2026.08.31-4057e58", true},
		{"cursor", "2026.08.31-4057e58", "2026.11.11-b912975", false},
		{"cursor", "2026.11.11-aaaaaaa", "2026.11.11-b912975", true},
		{"github", "v0.23.1", "v2026.9.24", true},
		{"github", "v0.23.1", "v0.23.1", false},
		{"npm", "nightly", "2.1.322", true},
	} {
		if got := Newer(c.eco, c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %q, %q) = %v, want %v", c.eco, c.a, c.b, got, c.want)
		}
	}
}

func TestEveryHarnessAndItsVariantsReachOnePin(t *testing.T) {
	t.Parallel()
	for target, arg := range map[string]string{
		"claudecode": "CLAUDE_CODE_VERSION", "claudecode-browser": "CLAUDE_CODE_VERSION", "claudecode-solidity": "CLAUDE_CODE_VERSION",
		"codex": "CODEX_VERSION", "codex-browser": "CODEX_VERSION",
		"cursor": "CURSOR_AGENT_VERSION", "cursor-browser": "CURSOR_AGENT_VERSION",
		"opencode": "OPENCODE_VERSION", "opencode-browser": "OPENCODE_VERSION",
		"hermes": "HERMES_AGENT_VERSION",
	} {
		if _, pin, ok := AgentSpec(target); !ok || pin.Arg != arg {
			t.Errorf("AgentSpec(%s) = %q, %v; want %s", target, pin.Arg, ok, arg)
		}
	}
	for _, base := range []string{"base", "base-node", "base-node-browser", "nope"} {
		if _, pin, ok := AgentSpec(base); ok {
			t.Errorf("AgentSpec(%s) found the pin %s; a base carries no agent", base, pin.Arg)
		}
	}
}

func TestOverlayRefKeepsTheRepoAndSpellsALegalTag(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ base, version, want string }{
		{"proveo/claudecode:latest", "2.1.330", "proveo/claudecode:agent-2.1.330"},
		{"proveo/cursor-browser:local", "2026.11.11-b912975", "proveo/cursor-browser:agent-2026.11.11-b912975"},
		{"registry.example:5000/team/codex:latest", "1.0.0+build/7", "registry.example:5000/team/codex:agent-1.0.0_build_7"},
	} {
		if got := OverlayRef(c.base, c.version); got != c.want {
			t.Errorf("OverlayRef(%q, %q) = %q, want %q", c.base, c.version, got, c.want)
		}
	}
}

func TestTheOverlayRecipeInstallsOverTheBaseAndRestoresItsUser(t *testing.T) {
	t.Parallel()
	s, pin, _ := AgentSpec("cursor")
	got := OverlayDockerfile(s, pin, "proveo/cursor:latest", "sha256:abc", "cursor")
	for _, want := range []string{
		"FROM proveo/cursor:latest\nUSER root\n",
		"ARG CURSOR_INSTALL_URL\nARG CURSOR_AGENT_VERSION\nRUN HOME=/opt/cursor-dist ",
		"\nUSER cursor\nLABEL proveo.agent.version=\"${CURSOR_AGENT_VERSION}\" \\\n      proveo.agent.overlay.base=\"sha256:abc\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the recipe lacks %q:\n%s", want, got)
		}
	}
	s, pin, _ = AgentSpec("claudecode-browser")
	got = OverlayDockerfile(s, pin, "proveo/claudecode-browser:local", "sha256:def", "")
	if strings.Count(got, "USER ") != 1 {
		t.Errorf("a base with no USER must stay root, got:\n%s", got)
	}
	if !strings.Contains(got, "RUN --mount=type=cache,id=proveo-agent-overlay-npm,target=/root/.npm,sharing=locked \\\n    npm install -g \"@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}\"") {
		t.Errorf("the npm overlay must install the pinned package through the cache mount:\n%s", got)
	}
}

func TestOverlayBuildsOnTheHostStoreWithoutPulling(t *testing.T) {
	t.Parallel()
	d := &fakeDocker{}
	b, _ := newTest(t, d, map[string]string{"CURSOR_INSTALL_URL": "https://mirror.example/install"})
	if err := b.Overlay("cursor-browser", "proveo/cursor-browser:latest", "sha256:abc", "cursor", "2026.11.11-b912975",
		"proveo/cursor-browser:agent-2026.11.11-b912975"); err != nil {
		t.Fatal(err)
	}
	builds := d.builds()
	if len(builds) != 1 {
		t.Fatalf("want one build, got %q", builds)
	}
	for _, want := range []string{
		"--builder default", "--platform linux/arm64", "--load", "--pull=false",
		"--build-arg CURSOR_AGENT_VERSION=2026.11.11-b912975",
		"--build-arg CURSOR_INSTALL_URL=https://mirror.example/install",
		"-t proveo/cursor-browser:agent-2026.11.11-b912975",
	} {
		if !strings.Contains(builds[0], want) {
			t.Errorf("the overlay build lacks %q: %s", want, builds[0])
		}
	}
	if strings.Contains(builds[0], "--cache-to") {
		t.Errorf("an overlay must not export a named cache: %s", builds[0])
	}
	f := strings.Fields(builds[0])
	if _, err := os.Stat(f[len(f)-1]); !os.IsNotExist(err) {
		t.Errorf("the overlay's build context %s outlived the build", f[len(f)-1])
	}
}

func TestOverlayRefusesWhatItCannotBuild(t *testing.T) {
	t.Parallel()
	b, _ := newTest(t, &fakeDocker{}, nil)
	if err := b.Overlay("hermes", "proveo/hermes:latest", "sha256:abc", "", "v0.23.1", "proveo/hermes:agent-v0.23.1"); !errors.Is(err, ErrNoOverlay) {
		t.Errorf("hermes is its upstream's whole image; Overlay = %v, want ErrNoOverlay", err)
	}
	container := &fakeDocker{driver: "docker-container"}
	b, _ = newTest(t, container, nil)
	err := b.Overlay("codex", "proveo/codex:local", "sha256:abc", "codex", "0.200.0", "proveo/codex:agent-0.200.0")
	if err == nil || len(container.builds()) != 0 {
		t.Errorf("a container-driver builder cannot read a local base; Overlay = %v, builds %q", err, container.builds())
	}
}
