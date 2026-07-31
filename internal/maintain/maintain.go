// Package maintain is the maintainer-side target registry and build/deploy/test
// orchestration: the single source of truth for which images the tooling
// operates on, where each is defined, and how each is built. Harness targets are
// drawn from the harness manifests (internal/manifest); the shared base image
// and the egress sidecars are fixed entries with no manifest. It is consumed by
// the `proveo targets|build|deploy|test` commands (cmd/proveo) — replacing the
// Bash lib/manifest-enum.sh, lib/runners.sh, and lib/{build,deploy,test}.sh.
//
// Plans are pure data ([]Command); cmd/proveo executes or prints them.
package maintain

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/proveo-ca/proveo/internal/manifest"
)

// Target kinds.
const (
	KindBase    = "base"
	KindHarness = "harness"
	KindSidecar = "sidecar"
)

// Target is one buildable/deployable image in the maintainer registry.
type Target struct {
	Name        string   // e.g. "claudecode-solo"
	Kind        string   // KindBase | KindHarness | KindSidecar
	Image       string   // org/name without a tag, e.g. "proveo/claudecode-solo"
	DefDir      string   // def directory holding build.sh / test.sh
	BuildScript string   // DefDir/build.sh
	BuildArgs   []string // extra build.sh flags — the variant selector (e.g. --variant mcp)
	TestScript  string   // DefDir/test.sh (may not exist; TestPlan checks at run time)
}

// sidecars are the fixed egress-enforcement images (no harness manifest). Name
// doubles as the defs/sidecars/<name> subdir and the proveo/<name> image.
var sidecars = []string{"egress-proxy", "mitmproxy"}

// variantArgs maps a target name to the build.sh flags that select its variant.
// claudecode ships three images from one def: mcp is the base "claudecode"
// image, solo drops MCP, sol layers a Solidity/security toolchain on mcp.
var variantArgs = map[string][]string{
	"claudecode":         {"--variant", "mcp"},
	"claudecode-solo":    {"--variant", "solo"},
	"claudecode-sol":     {"--variant", "sol"},
	"claudecode-browser": {"--browser"},
	"opencode-browser":   {"--browser"},
	"cursor-browser":     {"--browser"},
}

// Registry returns the maintainer targets in stable order: the base image, then
// the harness targets (sorted by name) from ms, then the egress sidecars.
// defsDir is the on-disk defs/ root; the base + sidecar DefDirs are joined onto
// it, while harness DefDirs come from each manifest's own Dir. build/test script
// paths are conventional (DefDir/{build,test}.sh); existence is checked at run.
func Registry(ms []manifest.Manifest, defsDir string) []Target {
	out := []Target{
		{Name: "base", Kind: KindBase, Image: "proveo/base", DefDir: filepath.Join(defsDir, "base")},
		{Name: "base-node", Kind: KindBase, Image: "proveo/base-node", DefDir: filepath.Join(defsDir, "base-node")},
		{Name: "base-node-lsp", Kind: KindBase, Image: "proveo/base-node-lsp", DefDir: filepath.Join(defsDir, "base-node-lsp")},
		{Name: "base-node-browser", Kind: KindBase, Image: "proveo/base-node-browser", DefDir: filepath.Join(defsDir, "base-node-browser")},
	}

	var harness []Target
	for _, m := range ms {
		for target, image := range m.Images {
			harness = append(harness, Target{
				Name:   target,
				Kind:   KindHarness,
				Image:  stripTag(image),
				DefDir: m.Dir,
			})
		}
	}
	sort.Slice(harness, func(i, j int) bool { return harness[i].Name < harness[j].Name })
	out = append(out, harness...)

	for _, name := range sidecars {
		out = append(out, Target{
			Name:   name,
			Kind:   KindSidecar,
			Image:  "proveo/" + name,
			DefDir: filepath.Join(defsDir, "sidecars", name),
		})
	}

	// Attach the build recipe to every target (uniform: paths off DefDir, plus
	// the per-target variant selector).
	for i := range out {
		out[i].BuildScript = filepath.Join(out[i].DefDir, "build.sh")
		out[i].TestScript = filepath.Join(out[i].DefDir, "test.sh")
		out[i].BuildArgs = variantArgs[out[i].Name]
	}
	return out
}

// Command is one step of a maintainer plan: argv run in Dir (empty => inherit).
// Quiet discards stdout (used for the verify `docker image inspect`, whose JSON
// is noise — only its exit code matters).
type Command struct {
	Dir   string
	Argv  []string
	Quiet bool
}

// BuildPlan builds the target and leaves it tagged :tag locally. defs/*/build.sh
// go through defs/lib/docker-build.sh (buildx): local builds --load the host
// platform; multi-arch publish is DeployPlan. For a non-latest tag the script
// receives --tag (no separate `docker tag` hop).
func (t Target) BuildPlan(tag string, noCache bool) []Command {
	tag = normTag(tag)
	build := append([]string{"bash", t.BuildScript}, t.BuildArgs...)
	if tag != "latest" {
		build = append(build, "--tag", tag)
	}
	if noCache {
		build = append(build, "--no-cache")
	}
	return []Command{
		{Dir: t.DefDir, Argv: build},
		{Argv: []string{"docker", "image", "inspect", t.Image + ":" + tag}, Quiet: true},
	}
}

// DeployPlan rebuilds with buildx --push for linux/amd64,linux/arm64 (see
// defs/lib/docker-build.sh / PROVEO_PLATFORMS). A plain `docker push` of a
// locally --load'd image cannot publish a multi-arch manifest.
func (t Target) DeployPlan(tag string) []Command {
	tag = normTag(tag)
	build := append([]string{"bash", t.BuildScript}, t.BuildArgs...)
	build = append(build, "--tag", tag, "--push")
	return []Command{{Dir: t.DefDir, Argv: build}}
}

// TestPlan runs the def's test.sh. It returns nil when the def has no test.sh —
// callers treat that as "skip". exists is injected so the decision stays pure.
func (t Target) TestPlan(exists func(string) bool) []Command {
	if t.TestScript == "" || !exists(t.TestScript) {
		return nil
	}
	return []Command{{Dir: t.DefDir, Argv: []string{"bash", t.TestScript}}}
}

// stripTag drops a trailing ":tag" from an image reference.
func stripTag(image string) string {
	if i := strings.IndexByte(image, ':'); i >= 0 {
		return image[:i]
	}
	return image
}

func normTag(tag string) string {
	if strings.TrimSpace(tag) == "" {
		return "latest"
	}
	return tag
}
