// SPEC: _spec/_devops/agent-version-pin.puml, _spec/_devops/agent-update-prompt.puml
package contract_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/imagebuild"
	"github.com/proveo-ca/proveo/internal/manifest"
)

var agentPins = []struct {
	dockerfile string         // repo-relative
	target     string         // imagebuild target that pins it
	arg        string         // build-arg name; also the maintainer override env var
	label      string         // proveo.agent label value
	ecosystem  string         // imagebuild.Pin.Eco
	source     string         // imagebuild.Pin.Pkg
	install    *regexp.Regexp // the pinned install, using the arg
	banned     []string       // spellings that would reintroduce @latest
	overlay    bool           // the agent installs over a built image
}{
	{
		dockerfile: "defs/opencode/Dockerfile", target: "opencode",
		arg: "OPENCODE_VERSION", label: "@opencode/cli", ecosystem: "npm", source: "@opencode/cli",
		install: regexp.MustCompile(`npm install -g "@opencode/cli@\$\{OPENCODE_VERSION\}"`),
		banned:  []string{"@opencode/cli@latest", "npm install -g @opencode/cli ", "opencode-ai"},
		overlay: true,
	},
	{
		dockerfile: "defs/claudecode/mcp/Dockerfile", target: "claudecode",
		arg: "CLAUDE_CODE_VERSION", label: "@anthropic-ai/claude-code", ecosystem: "npm", source: "@anthropic-ai/claude-code",
		install: regexp.MustCompile(`npm install -g "@anthropic-ai/claude-code@\$\{CLAUDE_CODE_VERSION\}"`),
		banned:  []string{"claude-code@latest", "npm install -g @anthropic-ai/claude-code "},
		overlay: true,
	},
	{
		dockerfile: "defs/codex/Dockerfile", target: "codex",
		arg: "CODEX_VERSION", label: "@openai/codex", ecosystem: "npm", source: "@openai/codex",
		install: regexp.MustCompile(`npm install -g "@openai/codex@\$\{CODEX_VERSION\}"`),
		banned:  []string{"codex@latest", "CODEX_VERSION=latest", "npm install -g @openai/codex "},
		overlay: true,
	},
	{
		dockerfile: "defs/cursor/Dockerfile", target: "cursor",
		arg: "CURSOR_AGENT_VERSION", label: "cursor-agent", ecosystem: "cursor", source: "https://cursor.com/install",
		install: regexp.MustCompile(`test -d "/opt/cursor-dist/\.local/share/cursor-agent/versions/\$\{CURSOR_AGENT_VERSION\}"`),
		overlay: true,
	},
	{
		dockerfile: "defs/hermes/Dockerfile", target: "hermes",
		arg: "HERMES_AGENT_VERSION", label: "hermes-agent", ecosystem: "github", source: "NousResearch/hermes-agent",
		install: regexp.MustCompile(`(?m)^FROM nousresearch/hermes-agent:\$\{HERMES_AGENT_VERSION\} AS hermes-upstream$`),
		banned:  []string{"hermes-agent:latest", "HERMES_AGENT_VERSION=v"},
	},
}

func TestEveryHarnessPinsItsAgentByABuildArgItUses(t *testing.T) {
	t.Parallel()
	for _, p := range agentPins {
		t.Run(p.target, func(t *testing.T) {
			t.Parallel()
			df := dockerfileBody(t, p.dockerfile)
			for _, want := range []*regexp.Regexp{
				// Bare: no default. A default is @latest with a different spelling.
				regexp.MustCompile(`(?m)^ARG ` + p.arg + `$`),
				regexp.MustCompile(`test -n "\$\{` + p.arg + `\}"`),
				p.install,
				regexp.MustCompile(`proveo\.agent="` + regexp.QuoteMeta(p.label) + `"`),
				regexp.MustCompile(`proveo\.agent\.version="\$\{` + p.arg + `\}"`),
				regexp.MustCompile(`SPEC: [^\n]*_spec/_devops/agent-version-pin\.puml`),
			} {
				if !want.MatchString(df) {
					t.Errorf("%s lacks %s", p.dockerfile, want)
				}
			}
			if regexp.MustCompile(`(?m)^ARG ` + p.arg + `=`).MatchString(df) {
				t.Errorf("%s gives %s a default — a bare `docker build` would bake whatever that names", p.dockerfile, p.arg)
			}
			for _, b := range p.banned {
				if strings.Contains(df, b) {
					t.Errorf("%s still installs the agent unpinned (%q) — the cache key never moves", p.dockerfile, b)
				}
			}

			spec := imagebuild.Specs[p.target]
			i := slices.IndexFunc(spec.Pins, func(pin imagebuild.Pin) bool {
				return pin.Arg == p.arg && pin.Eco == p.ecosystem && pin.Pkg == p.source
			})
			if i < 0 {
				t.Fatalf("imagebuild target %s does not pin %s with %s via %s", p.target, p.source, p.arg, p.ecosystem)
			}
			if slices.ContainsFunc(spec.EnvArgs, func(e imagebuild.EnvArg) bool { return e.Arg == p.arg }) {
				t.Errorf("imagebuild target %s also passes %s as a defaulted EnvArg, which is not a pin", p.target, p.arg)
			}
			overlay := spec.Pins[i].Overlay
			switch {
			case !p.overlay && overlay != "":
				t.Errorf("%s is its upstream's whole image, yet it has an overlay recipe: %q", p.target, overlay)
			case p.overlay && !p.install.MatchString(overlay):
				t.Errorf("the %s overlay does not run the Dockerfile's own install (%s):\n%s", p.target, p.install, overlay)
			case p.overlay && !strings.Contains(overlay, "${"+p.arg+"}"):
				t.Errorf("the %s overlay never reads %s:\n%s", p.target, p.arg, overlay)
			}
		})
	}
}

// SPEC: _spec/_devops/agent-update-prompt.puml
func TestEveryHarnessIsCoveredByTheAgentUpdateCheck(t *testing.T) {
	t.Parallel()
	pinned := map[string]bool{}
	for _, p := range agentPins {
		pinned[p.target] = true
	}
	for _, name := range harnessNames(t) {
		_, pin, ok := imagebuild.AgentSpec(name)
		if !ok {
			t.Errorf("harness %s has no agent pin: `proveo run %s` could never say its agent is behind", name, name)
			continue
		}
		if !pinned[name] {
			t.Errorf("harness %s pins %s but agentPins has no row for it, so nothing checks its Dockerfile", name, pin.Arg)
		}
	}
}

func harnessNames(t *testing.T) []string {
	t.Helper()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatalf("LoadFS(Manifests): %v", err)
	}
	var names []string
	for _, m := range ms {
		names = append(names, m.Name)
	}
	slices.Sort(names)
	return names
}
