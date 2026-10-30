// SPEC: _spec/internal/posture/one-value-two-renderings.puml, _spec/_devops/agent-version-pin.puml
package posture

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/runlog"
)

func TestWorkspaceHeaderStatesFacts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, f := range []string{"go.mod", "package.json", "nx.json", "main.go", "Dockerfile"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(dir, ".opencode")
	if err := os.MkdirAll(filepath.Join(cfg, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"architect.md", "sre.md"} {
		if err := os.WriteFile(filepath.Join(cfg, "agents", a), []byte("#"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(`{"hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	man := manifest.Manifest{Workspace: manifest.Workspace{ConfigDir: ".opencode"}}
	got := strings.Join(WorkspaceHeader(man, dir, dir, t.TempDir()), "\n")

	for _, want := range []string{"tooling:", "go", "nx", "node", "docker", "subagents: 2 definition(s)", ".opencode/settings.json"} {
		if !strings.Contains(got, want) {
			t.Errorf("header missing %q\n--- got ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "lsp:") {
		t.Errorf("the header must not carry an lsp: row, got:\n%s", got)
	}
}

func TestWorkspaceHeaderIsEmptyWithoutAWorkspace(t *testing.T) {
	t.Parallel()
	if got := WorkspaceHeader(manifest.Manifest{}, "", "", ""); got != nil {
		t.Errorf("no input dir must yield no header, got %v", got)
	}
}

func TestReadmePillsMatchToolingRegistry(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)

	pill := regexp.MustCompile(`!\[([a-z0-9.+-]+)\]\(https://img\.shields\.io/badge/`)
	inReadme := map[string]bool{}
	for _, m := range pill.FindAllStringSubmatch(readme, -1) {
		inReadme[m[1]] = true
	}
	if len(inReadme) == 0 {
		t.Fatal("no tooling pills found in README.md — the supported-tooling section is missing")
	}

	for _, label := range ToolingLabels() {
		if !inReadme[label] {
			t.Errorf("toolingMarkers has %q but README.md has no pill for it", label)
		}
		delete(inReadme, label)
	}
	for stale := range inReadme {
		t.Errorf("README.md has a pill for %q which is not in toolingMarkers", stale)
	}
}

func TestGlyphModeFromLookup(t *testing.T) {
	t.Parallel()
	cases := map[string]GlyphMode{
		"": GlyphsSafe, "safe": GlyphsSafe, "1": GlyphsSafe, "typo": GlyphsSafe,
		"nerd": GlyphsNerd, "nerdfont": GlyphsNerd,
		"ascii": GlyphsASCII, "ASCII": GlyphsASCII,
		"off": GlyphsOff, "0": GlyphsOff, "false": GlyphsOff, "none": GlyphsOff,
	}
	for in, want := range cases {
		if got := GlyphModeFrom(func(string) string { return in }); got != want {
			t.Errorf("PROVEO_GLYPHS=%q: got mode %d, want %d", in, got, want)
		}
	}
}

func TestEnforcedByNamesTheBoundaryHolder(t *testing.T) {
	t.Parallel()
	if got := EnforcedBy(); !strings.Contains(got, "sbx") || strings.Contains(got, "squid") {
		t.Errorf("the boundary must name sbx and no retired sidecar, got %q", got)
	}
}

func TestMCPGatewayPostureNamesTheDecision(t *testing.T) {
	// No t.Parallel: the last assertion sets an env var precisely to prove it is
	// ignored, and t.Setenv is incompatible with parallel tests.
	const gwVar = "MCP_GATEWAY_URL"
	if got := MCPGateway(false, gwVar); !strings.Contains(got, "declined") {
		t.Errorf("want the decline reported, got %q", got)
	}
	got := MCPGateway(true, gwVar)
	if !strings.Contains(got, "allowed") || !strings.Contains(got, "scope user") {
		t.Errorf("allowing it must say so AND name where it lands, got %q", got)
	}
	t.Setenv("PROVEO_SBX_MCP", "on")
	if MCPGateway(false, gwVar) != "declined ("+gwVar+" empty; the mcp row or PROVEO_SBX_MCP=on allows it)" {
		t.Error("the row read the environment instead of its argument")
	}
}

func TestWorkspacePostureNamesWhereWorkLands(t *testing.T) {
	t.Parallel()
	if got := Workspace(false); !strings.Contains(got, "mounted checkout") {
		t.Errorf("a normal run edits the checkout and must say so, got %q", got)
	}
	got := Workspace(true)
	for _, want := range []string{"clone", "never written", "git fetch"} {
		if !strings.Contains(got, want) {
			t.Errorf("clone posture is missing %q: %s", want, got)
		}
	}
}

func TestObservabilityNamesTheSandboxRecord(t *testing.T) {
	t.Parallel()
	got := Observability()
	if !strings.Contains(got, runlog.PolicyLogFile) || strings.Contains(got, "squid") || strings.Contains(got, "flows.ndjson") {
		t.Errorf("observability must name sbx's policy log and no retired record, got %q", got)
	}
}

func TestAgentVersionReadsTheLabelAndStaysSilentWhenItCannot(t *testing.T) {
	orig := dockerImageLabel
	t.Cleanup(func() { dockerImageLabel = orig })

	for _, tc := range []struct {
		name, label, want string
	}{
		{"the label is the version", "2.1.258", "2.1.258"},
		{"docker prints <no value> for a missing label", "<no value>", ""},
		{"no docker, no image, no answer", "", ""},
		{"whitespace is not a version", "  \n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dockerImageLabel = func(ref, label string) string {
				if label != AgentVersionLabel {
					t.Errorf("read label %q, want %q", label, AgentVersionLabel)
				}
				v := strings.TrimSpace(tc.label)
				if v == "<no value>" {
					return ""
				}
				return v
			}
			if got := AgentVersion("proveo/claudecode:local"); got != tc.want {
				t.Errorf("AgentVersion() = %q, want %q", got, tc.want)
			}
		})
	}
}
