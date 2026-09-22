// SPEC: _spec/_devops/agent-version-pin.puml
package contract_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/imagebuild"
)

var agentPins = []struct {
	image     string         // key into imageDockerfiles
	target    string         // imagebuild target that pins it
	arg       string         // build-arg name; also the maintainer override env var
	pkg       string         // proveo.agent label value
	ecosystem string         // proveo_agent_version ecosystem
	install   *regexp.Regexp // the pinned install, using the arg
	banned    []string       // spellings that would reintroduce @latest
}{
	{
		image: "proveo/opencode", target: "opencode",
		arg: "OPENCODE_VERSION", pkg: "opencode-ai", ecosystem: "npm",
		install: regexp.MustCompile(`npm install -g "opencode-ai@\$\{OPENCODE_VERSION\}"`),
		banned:  []string{"opencode-ai@latest", "npm install -g opencode-ai "},
	},
	{
		image: "proveo/claudecode", target: "claudecode",
		arg: "CLAUDE_CODE_VERSION", pkg: "@anthropic-ai/claude-code", ecosystem: "npm",
		install: regexp.MustCompile(`npm install -g "@anthropic-ai/claude-code@\$\{CLAUDE_CODE_VERSION\}"`),
		banned:  []string{"claude-code@latest", "npm install -g @anthropic-ai/claude-code "},
	},
	{
		image: "proveo/cecli", target: "cecli",
		arg: "CECLI_VERSION", pkg: "cecli-dev", ecosystem: "pypi",
		// Flags may sit between `install` and the spec (--no-compile keeps 153 MB of
		// bytecode out of the venv); the contract is that the SPEC names the arg.
		install: regexp.MustCompile(`pip install (?:--[^\s"]+ )*"cecli-dev==\$\{CECLI_VERSION\}"`),
		banned:  []string{"pip install cecli-dev "},
	},
	{
		image: "proveo/cursor", target: "cursor",
		arg: "CURSOR_AGENT_VERSION", pkg: "cursor-agent", ecosystem: "cursor",
		install: regexp.MustCompile(`test -d "/opt/cursor-dist/\.local/share/cursor-agent/versions/\$\{CURSOR_AGENT_VERSION\}"`),
	},
}

func TestEveryHarnessPinsItsAgentByABuildArgItUses(t *testing.T) {
	t.Parallel()
	for _, p := range agentPins {
		t.Run(p.image, func(t *testing.T) {
			t.Parallel()
			rel, ok := imageDockerfiles[p.image]
			if !ok {
				t.Fatalf("%s is not in imageDockerfiles", p.image)
			}
			df := dockerfileBody(t, rel)
			for _, want := range []*regexp.Regexp{
				// Bare: no default. A default is @latest with a different spelling.
				regexp.MustCompile(`(?m)^ARG ` + p.arg + `$`),
				regexp.MustCompile(`test -n "\$\{` + p.arg + `\}"`),
				p.install,
				regexp.MustCompile(`proveo\.agent="` + regexp.QuoteMeta(p.pkg) + `"`),
				regexp.MustCompile(`proveo\.agent\.version="\$\{` + p.arg + `\}"`),
				regexp.MustCompile(`SPEC: _spec/_devops/agent-version-pin\.puml`),
			} {
				if !want.MatchString(df) {
					t.Errorf("%s lacks %s", rel, want)
				}
			}
			for _, b := range p.banned {
				if strings.Contains(df, b) {
					t.Errorf("%s still installs the agent unpinned (%q) — the cache key never moves", rel, b)
				}
			}

			spec := imagebuild.Specs[p.target]
			if !slices.ContainsFunc(spec.Pins, func(pin imagebuild.Pin) bool { return pin.Arg == p.arg && pin.Eco == p.ecosystem }) {
				t.Errorf("imagebuild target %s does not pin %s via %s — the Dockerfile requires the arg, so a build without it fails", p.target, p.arg, p.ecosystem)
			}
		})
	}
}
