// SPEC: _spec/_plans/host-shell-to-go.puml, _spec/_devops/image-lineage-and-publish.puml, _spec/_devops/agent-version-pin.puml
package imagebuild

import (
	"fmt"
	"path/filepath"
	"sort"
)

// Pin is a build arg resolved to an upstream release.
type Pin struct {
	Arg, Eco, Pkg string
	PkgEnv        string // env var that, when set, replaces Pkg
}

// EnvArg is a build arg read from the environment with a default.
type EnvArg struct{ Arg, Default string }

// Spec is how one registry target is built.
type Spec struct {
	Repo       string
	Override   string // env var naming the image repo
	Dockerfile string // repo-relative; empty means the context's Dockerfile
	Context    string // repo-relative

	Parent         string // target ensured and passed as BASE_IMAGE
	ParentOverride string

	Needs         string // sibling that must exist: built if missing, published for --push
	NeedsOverride string
	Layer         string // USER_NAME for the browser layer built on Needs

	Pins    []Pin
	EnvArgs []EnvArg

	Floor     string // in-image probe a present image must pass
	FloorWhat string
}

const (
	baseFloor = `command -v git >/dev/null \
      && command -v gh >/dev/null \
      && command -v jq >/dev/null \
      && test -x /usr/local/bin/proveo-entrypoint`
	nodeFloor = `command -v git >/dev/null \
      && command -v jq >/dev/null \
      && command -v node >/dev/null \
      && command -v pnpm >/dev/null \
      && command -v bun >/dev/null \
      && command -v bunx >/dev/null \
      && test -x /usr/local/bin/proveo-entrypoint`
	lspFloor = `command -v node >/dev/null \
      && command -v jq >/dev/null \
      && command -v typescript-language-server >/dev/null \
      && command -v pyright-langserver >/dev/null`
	browserFloor = `command -v node >/dev/null \
      && command -v playwright >/dev/null \
      && command -v typescript-language-server >/dev/null \
      && ls "${PLAYWRIGHT_BROWSERS_PATH:-/opt/ms-playwright}"/chromium-* >/dev/null 2>&1 \
      && command -v agent-browser >/dev/null \
      && test -x "${AGENT_BROWSER_EXECUTABLE_PATH:-/opt/proveo/chromium/chrome}" \
      && test -f "${AGENT_BROWSER_SKILLS_DIR:-/opt/agent-browser/skill-data}/core/SKILL.md" \
      && test -f /opt/proveo/skills/agent-browser/SKILL.md`
)

const browserLayer = "defs/base-node-browser"

func browserOf(parent, user string) Spec {
	return Spec{
		Repo: "proveo/" + parent + "-browser", Override: envName(parent + "-browser"),
		Dockerfile: browserLayer + "/Dockerfile", Context: browserLayer,
		Needs: parent, NeedsOverride: envName(parent), Layer: user,
	}
}

const hermesBakedModelLayer = "defs/hermes/baked-model"

// hermesBakedModelOf bakes model into the hermes image at build time — no
// runtime pull, no Ollama sidecar. SPEC: _spec/defs/hermes/hermes-paradigm.puml
func hermesBakedModelOf(name, model string) Spec {
	return Spec{
		Repo: "proveo/" + name, Override: envName(name),
		Dockerfile: hermesBakedModelLayer + "/Dockerfile", Context: hermesBakedModelLayer,
		Needs: "hermes", NeedsOverride: envName("hermes"),
		EnvArgs: []EnvArg{{Arg: "OLLAMA_MODEL_TAG", Default: model}},
	}
}

func envName(target string) string {
	out := []byte("PROVEO_")
	for _, c := range []byte(target) {
		switch {
		case c == '-':
			out = append(out, '_')
		case c >= 'a' && c <= 'z':
			out = append(out, c-'a'+'A')
		default:
			out = append(out, c)
		}
	}
	return string(append(out, "_IMAGE"...))
}

// Specs is every buildable target, keyed by its maintain registry name.
var Specs = map[string]Spec{
	"base": {Repo: "proveo/base", Override: "PROVEO_BASE_IMAGE",
		Dockerfile: "defs/base/Dockerfile", Context: ".",
		Floor: baseFloor, FloorWhat: "the proveo/base floor"},
	"base-node": {Repo: "proveo/base-node", Override: "PROVEO_BASE_NODE_IMAGE",
		Dockerfile: "defs/base-node/Dockerfile", Context: "defs/base-node",
		Parent: "base", ParentOverride: "PROVEO_BASE_IMAGE",
		Floor: nodeFloor, FloorWhat: "the node/pnpm/bun floor"},
	"base-node-lsp": {Repo: "proveo/base-node-lsp", Override: "PROVEO_BASE_NODE_LSP_IMAGE",
		Dockerfile: "defs/base-node-lsp/Dockerfile", Context: "defs/base-node-lsp",
		Parent: "base-node", ParentOverride: "PROVEO_BASE_NODE_IMAGE",
		Floor: lspFloor, FloorWhat: "the LSP floor"},
	"base-node-browser": {Repo: "proveo/base-node-browser", Override: "PROVEO_BASE_NODE_BROWSER_IMAGE",
		Dockerfile: browserLayer + "/Dockerfile", Context: browserLayer,
		Parent: "base-node-lsp", ParentOverride: "PROVEO_BASE_NODE_LSP_IMAGE",
		Floor: browserFloor, FloorWhat: "the Playwright/Chromium/agent-browser floor"},

	"cecli": {Repo: "proveo/cecli", Override: "PROVEO_CECLI_IMAGE",
		Dockerfile: "defs/cecli/Dockerfile", Context: ".",
		Parent: "base", ParentOverride: "PROVEO_BASE_IMAGE",
		Pins: []Pin{{Arg: "CECLI_VERSION", Eco: "pypi", Pkg: "cecli-dev"},
			{Arg: "SERENA_VERSION", Eco: "pypi", Pkg: "serena-agent"}}},
	"claudecode": {Repo: "proveo/claudecode",
		Dockerfile: "defs/claudecode/mcp/Dockerfile", Context: ".",
		Parent: "base-node-lsp", ParentOverride: "PROVEO_BASE_NODE_LSP_IMAGE",
		Pins: []Pin{{Arg: "CLAUDE_CODE_VERSION", Eco: "npm", Pkg: "@anthropic-ai/claude-code"}}},
	"claudecode-solidity": {Repo: "proveo/claudecode-solidity",
		Dockerfile: "defs/claudecode/solidity/Dockerfile", Context: ".",
		Needs: "claudecode"},
	"codex": {Repo: "proveo/codex", Override: "PROVEO_CODEX_IMAGE",
		Dockerfile: "defs/codex/Dockerfile", Context: ".",
		Parent: "base-node-lsp", ParentOverride: "PROVEO_BASE_NODE_LSP_IMAGE",
		EnvArgs: []EnvArg{{Arg: "CODEX_VERSION", Default: "latest"}}},
	"cursor": {Repo: "proveo/cursor", Override: "PROVEO_CURSOR_IMAGE",
		Dockerfile: "defs/cursor/Dockerfile", Context: ".",
		Parent: "base", ParentOverride: "PROVEO_BASE_IMAGE",
		EnvArgs: []EnvArg{{Arg: "CURSOR_INSTALL_URL", Default: "https://cursor.com/install"}},
		Pins:    []Pin{{Arg: "CURSOR_AGENT_VERSION", Eco: "cursor", Pkg: "https://cursor.com/install", PkgEnv: "CURSOR_INSTALL_URL"}}},
	"opencode": {Repo: "proveo/opencode", Override: "PROVEO_OPENCODE_IMAGE",
		Dockerfile: "defs/opencode/Dockerfile", Context: ".",
		Parent: "base-node-lsp", ParentOverride: "PROVEO_BASE_NODE_LSP_IMAGE",
		Pins: []Pin{{Arg: "OPENCODE_VERSION", Eco: "npm", Pkg: "opencode-ai"}}},
	// hermes has no Parent, deliberately: it builds FROM nousresearch/hermes-agent
	// directly rather than proveo's own base chain, which would duplicate what
	// upstream's image already bakes. SPEC: _spec/defs/hermes/hermes-paradigm.puml
	"hermes": {Repo: "proveo/hermes", Override: "PROVEO_HERMES_IMAGE",
		Dockerfile: "defs/hermes/Dockerfile", Context: ".",
		EnvArgs: []EnvArg{{Arg: "HERMES_AGENT_VERSION", Default: "v2026.9.24"}}},
	"hermes-muse-glimmer": hermesBakedModelOf("hermes-muse-glimmer", "muse-glimmer:30b-nvfp4"),
	"hermes-qwen3.8":      hermesBakedModelOf("hermes-qwen3.8", "qwen3.8:27b-q4_K_M"),

	"claudecode-browser": func() Spec { s := browserOf("claudecode", "claude"); s.Override, s.NeedsOverride = "", ""; return s }(),
	"codex-browser":      browserOf("codex", "codex"),
	"cursor-browser":     browserOf("cursor", "cursor"),
	"opencode-browser":   browserOf("opencode", "opencode"),

	"egress-proxy": {Repo: "proveo/egress-proxy", Override: "PROVEO_EGRESS_PROXY_IMAGE",
		Dockerfile: "defs/sidecars/egress-proxy/Dockerfile", Context: "."},
	"mitmproxy": {Repo: "proveo/mitmproxy", Override: "PROVEO_MITMPROXY_IMAGE",
		Context: "defs/sidecars/mitmproxy"},
}

// SpecNames is every target in Specs, sorted.
func SpecNames() []string {
	out := make([]string, 0, len(Specs))
	for n := range Specs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Options are the per-build switches.
type Options struct{ Push, NoCache bool }

// TargetRef is the image a target builds at tag.
func (b *Builder) TargetRef(name, tag string) (string, error) {
	s, ok := Specs[name]
	if !ok {
		return "", fmt.Errorf("no build spec for target %q", name)
	}
	repo := s.Repo
	if v := b.env(s.Override); s.Override != "" && v != "" {
		repo = RefRepo(v)
	}
	if tag == "" {
		tag = "latest"
	}
	return repo + ":" + tag, nil
}

// BuildTarget builds one registry target at tag.
func (b *Builder) BuildTarget(name, tag string, o Options) error {
	ref, err := b.TargetRef(name, tag)
	if err != nil {
		return err
	}
	return b.buildRef(name, ref, o)
}

func (b *Builder) present(ref string) bool {
	_, err := b.Docker.Output("image", "inspect", ref)
	return err == nil
}

func (b *Builder) refFor(target, override, tag string) string {
	return b.ImageRef(override, Specs[target].Repo, tag)
}

func (b *Builder) buildRef(name, ref string, o Options) error {
	s, ok := Specs[name]
	if !ok {
		return fmt.Errorf("no build spec for target %q", name)
	}
	tag := RefTag(ref)
	var args []string
	if o.NoCache {
		args = append(args, "--no-cache")
	}
	from := ""
	if s.Needs != "" {
		from = b.refFor(s.Needs, s.NeedsOverride, tag)
		if o.Push {
			if err := b.RequirePublished(from, tag); err != nil {
				return err
			}
		} else if !b.present(from) {
			if err := b.BuildTarget(s.Needs, tag, Options{NoCache: o.NoCache}); err != nil {
				return err
			}
		}
	}
	if s.Parent != "" {
		from = b.refFor(s.Parent, s.ParentOverride, tag)
		if err := b.ensure(s.Parent, from, o.Push); err != nil {
			return err
		}
	}
	if from != "" {
		args = append(args, "--build-arg", "BASE_IMAGE="+from)
	}
	if s.Layer != "" {
		args = append(args, "--build-arg", "USER_NAME="+s.Layer)
	}
	for _, e := range s.EnvArgs {
		v := b.env(e.Arg)
		if v == "" {
			v = e.Default
		}
		args = append(args, "--build-arg", e.Arg+"="+v)
	}
	for _, p := range s.Pins {
		pkg := p.Pkg
		if v := b.env(p.PkgEnv); p.PkgEnv != "" && v != "" {
			pkg = v
		}
		v, err := b.AgentVersion(p.Arg, p.Eco, pkg)
		if err != nil {
			return err
		}
		args = append(args, "--build-arg", p.Arg+"="+v)
	}
	if s.Dockerfile != "" {
		args = append(args, "-f", filepath.Join(b.RepoRoot, s.Dockerfile))
	}
	args = append(args, "-t", ref, filepath.Join(b.RepoRoot, s.Context))
	if from != "" {
		b.UI.Appf("building %s from %s", ref, from)
	} else {
		b.UI.Appf("building %s", ref)
	}
	return b.Build(o.Push, args)
}

func (b *Builder) floor(ref, probe string) bool {
	_, err := b.Docker.Output("run", "--rm", "--entrypoint", "sh", ref, "-c", probe)
	return err == nil
}

func (b *Builder) ensure(name, ref string, push bool) error {
	s := Specs[name]
	if push {
		return b.RequirePublished(ref, RefTag(ref))
	}
	if b.present(ref) {
		if s.Floor == "" || b.floor(ref, s.Floor) {
			return nil
		}
		b.UI.Warnf("%s is present but missing %s — rebuilding", ref, s.FloorWhat)
		return b.buildRef(name, ref, Options{})
	}
	b.UI.Cloudf("%s missing — pulling", ref)
	if _, err := b.Docker.Output("pull", ref); err == nil && (s.Floor == "" || b.floor(ref, s.Floor)) {
		return nil
	}
	b.UI.Notef("pull failed or %s lacks %s — building from source", ref, s.FloorWhat)
	return b.buildRef(name, ref, Options{})
}
