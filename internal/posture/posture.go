// SPEC: _spec/internal/posture/one-value-two-renderings.puml Package posture
// renders the decisions a run has already made — once, for two readers: the
// header a human reads before launch, and the run-log block a human reads after
// a failure.
//
// SPEC: _spec/internal/posture/one-value-two-renderings.puml
package posture

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/maintain"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/runlog"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/wsscan"
)

func Image(ref string) string {
	if maintain.RefTag(ref) == maintain.LocalTag {
		return ref + " (local build)"
	}
	return ref + " (published)"
}

var dockerImageCreated = func(ref string) (time.Time, bool) {
	out, err := exec.Command("docker", "image", "inspect", ref, "--format", "{{.Created}}").Output()
	if err != nil {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(out)))
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}

func ResolveImageChoice(ref string) (chosen string, isLocal bool) {
	return maintain.ResolveImage(ref, dockerImageCreated)
}

var toolingMarkers = []wsscan.Marker{
	{Label: "go", Names: []string{"go.mod", "go.work"}, Suffixes: []string{".go"}},
	{Label: "node", Names: []string{"package.json"}},
	{Label: "nx", Names: []string{"nx.json"}},
	{Label: "turbo", Names: []string{"turbo.json"}},
	{Label: "mise", Names: []string{"mise.toml", ".mise.toml", ".tool-versions"}},
	{Label: "python", Names: []string{"pyproject.toml", "requirements.txt"}, Suffixes: []string{".py"}},
	{Label: "rust", Names: []string{"Cargo.toml"}},
	{Label: "docker", Names: []string{"Dockerfile", "compose.yml", "docker-compose.yml"}},
}

var lspMarkers = []wsscan.Marker{
	{Label: "gopls", Names: []string{"go.mod"}, Suffixes: []string{".go"}},
	{Label: "typescript-language-server", Names: []string{"tsconfig.json", "package.json"}, Suffixes: []string{".ts", ".tsx"}},
	{Label: "pyright-langserver", Names: []string{"pyproject.toml"}, Suffixes: []string{".py"}},
	{Label: "bash-language-server", Suffixes: []string{".sh"}},
	{Label: "docker-langserver", Names: []string{"Dockerfile"}},
	{Label: "yaml-language-server", Suffixes: []string{".yml", ".yaml"}},
}

// GlyphMode selects what decorates the lsp: row.
type GlyphMode = ui.GlyphTier

const (
	GlyphsNerd  = ui.GlyphsNerd // default
	GlyphsASCII = ui.GlyphsASCII
	GlyphsOff   = ui.GlyphsOff
)

var lspNerd = map[string]string{
	"gopls":                      "\ue627",
	"typescript-language-server": "\ue628",
	"pyright-langserver":         "\ue73c",
	"bash-language-server":       "\ue795",
	"docker-langserver":          "\ue7b0",
	"yaml-language-server":       "\ue60b",
}

var lspASCII = map[string]string{
	"gopls":                      "<>",
	"typescript-language-server": "<>",
	"pyright-langserver":         "<>",
	"bash-language-server":       "$ ",
	"docker-langserver":          "# ",
	"yaml-language-server":       "{}",
}

func GlyphModeFrom(lookup func(string) string) GlyphMode { return ui.GlyphTierFrom(lookup) }

func WithGlyphs(labels []string, mode GlyphMode) []string {
	if mode == GlyphsOff {
		return labels
	}
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		g, ok := "", false
		if mode == GlyphsNerd {
			g, ok = lspNerd[l]
		}
		if !ok {
			g, ok = lspASCII[l]
		}
		if !ok {
			out = append(out, l)
			continue
		}
		out = append(out, g+" "+l)
	}
	return out
}

func WorkspaceHeader(man manifest.Manifest, inputDir, repoRoot, homeRoot string, mode GlyphMode) []string {
	if inputDir == "" {
		return nil
	}
	var out []string
	tools := wsscan.Scan(inputDir, repoRoot, toolingMarkers, 0)
	if labels := tools.Labels(toolingMarkers); len(labels) > 0 {
		out = append(out, "tooling:  "+strings.Join(labels, "  "))
	}
	lsp := wsscan.Scan(inputDir, repoRoot, lspMarkers, 0)
	if labels := lsp.Labels(lspMarkers); len(labels) > 0 {
		out = append(out, "lsp:      "+strings.Join(WithGlyphs(labels, mode), "  "))
	}
	if tools.Truncated || lsp.Truncated {
		ui.Warnf("workspace scan hit its entry budget under %s — tooling/LSP lines may be incomplete", inputDir)
	}
	if n := countAgents(man, inputDir, homeRoot); n > 0 {
		out = append(out, fmt.Sprintf("subagents: %d definition(s)", n))
	}
	if hooks := DetectHooks(man, inputDir, homeRoot); len(hooks) > 0 {
		out = append(out, "hooks:    "+strings.Join(hooks, "  "))
	}
	return out
}

func agentDirs(man manifest.Manifest, inputDir, homeRoot string) []string {
	var dirs []string
	if cd := man.Workspace.ConfigDir; cd != "" {
		dirs = append(dirs, filepath.Join(inputDir, cd, "agents"))
	}
	for _, m := range man.Home.Mounts {
		dirs = append(dirs, filepath.Join(homeRoot, m.Host, "agents"))
	}
	return dirs
}

func countAgents(man manifest.Manifest, inputDir, homeRoot string) int {
	n := 0
	for _, d := range agentDirs(man, inputDir, homeRoot) {
		m, _ := filepath.Glob(filepath.Join(d, "*.md"))
		n += len(m)
	}
	return n
}

func DetectHooks(man manifest.Manifest, inputDir, homeRoot string) []string {
	var out []string
	if cd := man.Workspace.ConfigDir; cd != "" {
		for _, f := range []string{"settings.json", "settings.local.json"} {
			if b, err := os.ReadFile(filepath.Join(inputDir, cd, f)); err == nil && strings.Contains(string(b), `"hooks"`) {
				out = append(out, cd+"/"+f)
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(inputDir, ".git", "hooks")); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".sample") {
				out = append(out, "git/"+e.Name())
			}
		}
	}
	return out
}

func Workspace(clone bool) string {
	if clone {
		return "in-container clone (default on sbx; --clone=false opts out) — the checkout is never written; the agent's commits are `git fetch`ed back at teardown under refs/proveo/<sid>/"
	}
	return "mounted checkout — the agent edits it directly"
}

func MCPGateway(sandbox, allowed bool, gatewayVar string) string {
	switch {
	case !sandbox:
		return "n/a (docker backend)"
	case allowed:
		return "allowed (PROVEO_SBX_MCP=on) — sbx registers it into the proveo home, --scope user"
	}
	return "declined (" + gatewayVar + " empty; PROVEO_SBX_MCP=on to allow)"
}

func EnforcedBy(sandboxed bool) string {
	if sandboxed {
		return "sbx — Kit network allowlist + credential proxy (proveo runs no Squid or MITM)"
	}
	return "proveo — squid + mitmproxy sidecars on the session network"
}

func Observability(mode, credentials string, sandboxed bool) string {
	if sandboxed {
		return "sbx/" + runlog.PolicyLogFile + " — the daemon's allowed/blocked hosts (no MITM, so no DLP or body scan)"
	}
	if mode == "open" && credentials == "forward" {
		return "none — plain bridge, no MITM, no Squid: provider errors are NOT proveo denials"
	}
	if mode == "open" {
		return "flows.ndjson (MITM only, no allowlist)"
	}
	return "flows.ndjson + squid access.log"
}

func MergeRoles(explicit provider.Roles, remembered map[string]string) provider.Roles {
	out := provider.Roles{}
	for k, v := range explicit {
		out[k] = v
	}
	for k, v := range provider.RolesFromCanonical(remembered) {
		if _, set := out[k]; !set {
			out[k] = v
		}
	}
	return out
}

func RolesLine(bridges provider.BridgeTable, harness string, r provider.Roles) string {
	var parts []string
	for _, s := range bridges.EffectiveSlots(harness, r) {
		if p := provider.ModelProvider(s.Model); p != "" {
			parts = append(parts, fmt.Sprintf("%s=%s (%s)", s.Name, s.Model, p))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", s.Name, s.Model))
	}
	return strings.Join(parts, "  ")
}

// Posture is every row a run resolves, held as ONE value.
type Posture struct {
	Target         string
	EgressTier     string
	Credentials    string
	AddOns         string
	AgentEvidence  string
	DetectedKeys   string
	Brokered       string
	ReachableHosts string
	HarnessHosts   string
	AuthVar        string
	LocalModel     string
	Observability  string
	EnforcedBy     string
	Image          string
	ModelRoles     string
	RoleProviders  string
	MCPGateway     string
	Workspace      string
}

// Fields renders the run-log block. runlog.Log.Fields sorts the keys, so the
// ORDER is already stable; what was not pinned is the SET, which is what the
// posture golden covers.
func (p Posture) Fields() map[string]string {
	return map[string]string{
		"target":          p.Target,
		"egress tier":     p.EgressTier,
		"credentials":     p.Credentials,
		"add-ons":         p.AddOns,
		"agent evidence":  p.AgentEvidence,
		"detected keys":   p.DetectedKeys,
		"brokered":        p.Brokered,
		"reachable hosts": p.ReachableHosts,
		"harness hosts":   p.HarnessHosts,
		"auth var":        p.AuthVar,
		"local model":     p.LocalModel,
		"observability":   p.Observability,
		"enforced by":     p.EnforcedBy,
		"image":           p.Image,
		"model roles":     p.ModelRoles,
		"role providers":  p.RoleProviders,
		"sbx mcp gateway": p.MCPGateway,
		"workspace":       p.Workspace,
	}
}

// Render is the run-log block exactly as runlog would write it: keys sorted, an
// empty value shown as "(unset)".
func (p Posture) Render() string {
	f := p.Fields()
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		v := f[k]
		if v == "" {
			v = "(unset)"
		}
		fmt.Fprintf(&b, "  %-22s %s\n", k, v)
	}
	return b.String()
}

func ToolingLabels() []string {
	out := make([]string, 0, len(toolingMarkers))
	for _, m := range toolingMarkers {
		out = append(out, m.Label)
	}
	return out
}
