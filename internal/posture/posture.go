// SPEC: _spec/internal/posture/one-value-two-renderings.puml
//
// Package posture renders the decisions a run has already made — once, for two
// readers: the header a human reads before launch, and the run-log block a human
// reads after a failure. Assembling those independently is how a row could name
// Squid logs on a backend that runs none; one value rendered twice cannot.
//
// Nothing here decides anything. Every input arrives as a value, so a row is never
// a second place a decision gets made.
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

// Image records WHICH image a run took, because "proveo/claudecode:latest"
// alone does not distinguish the build under test from a registry artifact that may
// be weeks older.
func Image(ref string) string {
	if maintain.RefTag(ref) == maintain.LocalTag {
		return ref + " (local build)"
	}
	return ref + " (published)"
}

// dockerImageCreated reports when the host built or pulled an image.
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

// reportImageChoice resolves :latest against a local build and NAMES the winner.
// The naming is the point: an image silently resolving to a published artifact
// instead of the build under test is invisible until something behaves like code
// nobody wrote, and reading it off one header line is the whole difference.
// ResolveImageChoice returns the image the run will use and whether it is a local
// build. It no longer prints: a package that renders a posture should not also be
// a place output happens, or its tests cannot assert what a run says.
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

// GlyphMode selects what decorates the lsp: row. Nerd is the default and ASCII is the
// fallback an operator selects when their font stops at the Powerline range, because a
// terminal offers no way to ask whether its font carries a codepoint.
//
// An ALIAS of ui.GlyphTier rather than a type of its own. The tier is one decision
// per session, and three packages each holding their own idea of it is what let
// PROVEO_GLYPHS=off reach the topology figure and come back with nerd runes.
type GlyphMode = ui.GlyphTier

const (
	GlyphsNerd  = ui.GlyphsNerd // default
	GlyphsASCII = ui.GlyphsASCII
	GlyphsOff   = ui.GlyphsOff
)

// lspNerd maps an LSP server to its Nerd Font devicon — per-language identity, since
// a logo is recognised before it is read.
var lspNerd = map[string]string{
	"gopls":                      "\ue627",
	"typescript-language-server": "\ue628",
	"pyright-langserver":         "\ue73c",
	"bash-language-server":       "\ue795",
	"docker-langserver":          "\ue7b0",
	"yaml-language-server":       "\ue60b",
}

// lspASCII maps an LSP server to a category marker, deliberately coarser than the
// devicons: an ASCII symbol has to be decoded rather than recognised, so per-language
// distinctions it cannot carry are not worth inventing. Every marker is padded to two
// columns so the server names stay aligned whichever category they fall in.
//
// The set avoids "[", "(" and ">" on purpose. choiceui.go draws "[x] "/"[ ] " for
// checkboxes, "(•) "/"( ) " for radios, and "◀ riskier"/"safer ▶" for the legend, so
// a "[]" before a server name would read as an unchecked add-on rather than a glyph.
var lspASCII = map[string]string{
	"gopls":                      "<>",
	"typescript-language-server": "<>",
	"pyright-langserver":         "<>",
	"bash-language-server":       "$ ",
	"docker-langserver":          "# ",
	"yaml-language-server":       "{}",
}

// GlyphModeFrom reads PROVEO_GLYPHS through lookup, so a project .env can set it once
// per repo. Unset means nerd; an unrecognised value also means nerd rather than off,
// so a typo degrades to the default rather than silently stripping the row.
func GlyphModeFrom(lookup func(string) string) GlyphMode { return ui.GlyphTierFrom(lookup) }

// WithGlyphs prefixes each label with its glyph. Nerd mode falls back to the ASCII
// category marker for a server with no devicon, so adding a language to lspMarkers
// degrades to a category rather than to a ragged column. A server in neither table is
// left bare: an invented placeholder would read as a language nobody has.
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

// Workspace says whether the agent edits the operator's checkout or a
// private clone of it. It is a posture row because it changes WHERE the work
// lands, which is the one thing an operator must not have to guess.
func Workspace(clone bool) string {
	if clone {
		return "in-container clone (default on sbx; --clone=false opts out) — the checkout is never written; the agent's commits are `git fetch`ed back at teardown under refs/proveo/<sid>/"
	}
	return "mounted checkout — the agent edits it directly"
}

// MCPGateway reports what the run does about sbx's MCP gateway.
//
// It earns a posture row because it is a CAPABILITY the agent gets, decided
// outside the Kit: sbx registers the gateway from its own agent kit, into a HOME
// proveo mounts read-write. A posture that lists reachable hosts and credentials
// but not an MCP server the agent is told to call is not describing the run.
// MCPGateway takes both the backend and the gateway decision as VALUES. It used
// to read PROVEO_SBX_MCP itself, which made the row a second place the decision
// was made rather than a rendering of the one the run already made.
func MCPGateway(sandbox, allowed bool, gatewayVar string) string {
	switch {
	case !sandbox:
		return "n/a (docker backend)"
	case allowed:
		return "allowed (PROVEO_SBX_MCP=on) — sbx registers it into the proveo home, --scope user"
	}
	return "declined (" + gatewayVar + " empty; PROVEO_SBX_MCP=on to allow)"
}

// EnforcedBy names who holds the boundary. proveo's tiers describe its own Squid and
// MITM sidecars; under sbx neither runs, and the Kit hands enforcement to the sandbox
// runtime instead. Printing a tier without naming the enforcer reads as a proveo
// guarantee that proveo is not, on that backend, in a position to make.
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

// RolesLine renders the role assignment for the transcript and the prompt header,
// naming the slots the harness will actually fill rather than the role vars the
// operator happened to set. A harness that reads two of the three must not be shown
// advertising the third: the header is the only pre-launch view of that resolution.
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

// Posture is every row a run resolves, held as ONE value. It exists because the
// header and the run-log block used to be assembled independently from the same
// scattered locals, so the two could disagree — and did: a row named Squid logs on
// a backend that runs no Squid. A single struct makes that class unrepresentable.
//
// Every field is already-rendered text. Nothing here decides; the run decides and
// this reports, which is why the package imports no docker, no sbx and no cobra.
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
// empty value shown as "(unset)". It is what the posture golden asserts, so a row
// that silently appears, vanishes or changes wording fails a test instead of
// quietly changing what an operator is told.
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
