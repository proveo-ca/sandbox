// SPEC: _spec/internal/posture/one-value-two-renderings.puml
package posture

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/runlog"
)

func TestWorkspaceHeaderStatesFactsAndListsLSP(t *testing.T) {
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
	got := strings.Join(WorkspaceHeader(man, dir, dir, t.TempDir(), GlyphsOff), "\n")

	for _, want := range []string{"tooling:", "go", "nx", "node", "docker", "subagents: 2 definition(s)", ".opencode/settings.json"} {
		if !strings.Contains(got, want) {
			t.Errorf("header missing %q\n--- got ---\n%s", want, got)
		}
	}
	if !strings.Contains(got, "lsp:      gopls") {
		t.Errorf("LSP row must list the servers plainly, got:\n%s", got)
	}
	// The "will start" prefix was dropped deliberately (_spec/internal/choiceui/wireframe.puml).
	for _, banned := range []string{"lsp:      detected", "will start"} {
		if strings.Contains(got, banned) {
			t.Errorf("LSP row must state servers plainly; found %q in:\n%s", banned, got)
		}
	}
}

func TestWorkspaceHeaderIsEmptyWithoutAWorkspace(t *testing.T) {
	t.Parallel()
	if got := WorkspaceHeader(manifest.Manifest{}, "", "", "", GlyphsOff); got != nil {
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

func TestLSPMarkerLabelsAreRealServerBinaries(t *testing.T) {
	t.Parallel()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wd, "..", "..", "packages", "lib", "entrypoint-lib.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	arm := regexp.MustCompile(`(?m)^\s*[a-z]+\)\s+echo\s+"([^"\s]+)`)
	binaries := map[string]bool{}
	for _, m := range arm.FindAllStringSubmatch(string(data), -1) {
		binaries[m[1]] = true
	}
	if len(binaries) == 0 {
		t.Fatalf("no _lsp_server arms parsed from %s", path)
	}

	for _, m := range lspMarkers {
		if !binaries[m.Label] {
			t.Errorf("lspMarkers predicts %q, which is not a command in _lsp_server(); "+
				"the host would promise a binary the image never installs", m.Label)
		}
	}
}

func TestLSPGlyphModes(t *testing.T) {
	t.Parallel()
	labels := []string{"typescript-language-server", "bash-language-server", "gopls"}

	if got := WithGlyphs(labels, GlyphsOff); !reflect.DeepEqual(got, labels) {
		t.Errorf("glyphs off must not touch the labels, got %v", got)
	}

	for _, mode := range []GlyphMode{GlyphsNerd, GlyphsASCII} {
		got := WithGlyphs(labels, mode)
		for i, l := range labels {
			if !strings.HasSuffix(got[i], l) {
				t.Errorf("mode %d: %q must keep its server name, got %q", mode, l, got[i])
			}
			if got[i] == l {
				t.Errorf("mode %d: %q should have gained a glyph", mode, l)
			}
		}
	}

	// Nerd falls back to the ASCII category marker rather than leaving a hole.
	delete(lspNerd, "gopls")
	defer func() { lspNerd["gopls"] = "\ue627" }()
	if got := WithGlyphs([]string{"gopls"}, GlyphsNerd); got[0] != lspASCII["gopls"]+" gopls" {
		t.Errorf("a server with no devicon must fall back to ASCII, got %q", got[0])
	}

	// A server in neither table stays bare.
	if got := WithGlyphs([]string{"unknown-langserver"}, GlyphsNerd); got[0] != "unknown-langserver" {
		t.Errorf("unmapped server must stay bare, got %q", got[0])
	}
}

// Every language the scanner can detect needs a category marker, or enabling ASCII
// silently produces a column where some rows are indented and others are not.

// Every language the scanner can detect needs a category marker, or enabling ASCII
// silently produces a column where some rows are indented and others are not.
func TestEveryLSPMarkerHasAnASCIIGlyph(t *testing.T) {
	t.Parallel()
	for _, m := range lspMarkers {
		if _, ok := lspASCII[m.Label]; !ok {
			t.Errorf("%s has no ASCII category marker", m.Label)
		}
	}
	// ASCII markers pad to two columns so names stay aligned across categories.
	for label, g := range lspASCII {
		if len([]rune(g)) != 2 {
			t.Errorf("%s marker %q is %d cols; must be 2", label, g, len([]rune(g)))
		}
	}
}

func TestGlyphModeFromLookup(t *testing.T) {
	t.Parallel()
	cases := map[string]GlyphMode{
		"": GlyphsNerd, "nerd": GlyphsNerd, "1": GlyphsNerd, "typo": GlyphsNerd,
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
	if got := EnforcedBy(true); !strings.Contains(got, "sbx") || !strings.Contains(got, "no Squid") {
		t.Errorf("sbx runs must name sbx and disclaim proveo's sidecars, got %q", got)
	}
	if got := EnforcedBy(false); !strings.Contains(got, "squid") {
		t.Errorf("docker runs must name proveo's sidecars, got %q", got)
	}
}

func TestMCPGatewayPostureNamesTheDecision(t *testing.T) {
	// No t.Parallel: the last assertion sets an env var precisely to prove it is
	// ignored, and t.Setenv is incompatible with parallel tests.
	const gwVar = "MCP_GATEWAY_URL"
	if got := MCPGateway(false, false, gwVar); !strings.Contains(got, "n/a") {
		t.Errorf("the docker backend has no sbx gateway, got %q", got)
	}
	if got := MCPGateway(true, false, gwVar); !strings.Contains(got, "declined") {
		t.Errorf("want the decline reported, got %q", got)
	}
	got := MCPGateway(true, true, gwVar)
	if !strings.Contains(got, "allowed") || !strings.Contains(got, "scope user") {
		t.Errorf("allowing it must say so AND name where it lands, got %q", got)
	}
	t.Setenv("PROVEO_SBX_MCP", "on")
	if MCPGateway(true, false, gwVar) != "declined ("+gwVar+" empty; PROVEO_SBX_MCP=on to allow)" {
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

func TestObservabilityNamesTheBackendsOwnEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, mode, creds string
		sandboxed         bool
		want, wantNot     string
	}{
		{"sbx names its own record", "allowlist", "broker", true, runlog.PolicyLogFile, "squid"},
		{"sbx says so on every tier", "open", "forward", true, runlog.PolicyLogFile, "flows.ndjson"},
		{"docker allowlist keeps both logs", "allowlist", "broker", false, "squid access.log", ""},
		{"docker open keeps the MITM record", "open", "broker", false, "flows.ndjson", "squid"},
		{"docker open+forward promises nothing", "open", "forward", false, "none", "flows.ndjson"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Observability(tc.mode, tc.creds, tc.sandboxed)
			if !strings.Contains(got, tc.want) {
				t.Errorf("Observability(%q, %q, %v) = %q, want it to name %q",
					tc.mode, tc.creds, tc.sandboxed, got, tc.want)
			}
			if tc.wantNot != "" && strings.Contains(got, tc.wantNot) {
				t.Errorf("Observability(%q, %q, %v) = %q, must not name %q",
					tc.mode, tc.creds, tc.sandboxed, got, tc.wantNot)
			}
		})
	}
}
