// Plans are pure data ([]Command); cmd/proveo executes or prints them.
//
// SPEC: _spec/internal/maintain/image-build-deploy.puml, _spec/_devops/image-lineage-and-publish.puml
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
	Name        string   // e.g. "claudecode-solidity"
	Kind        string   // KindBase | KindHarness | KindSidecar
	Image       string   // org/name without a tag, e.g. "proveo/claudecode-solidity"
	DefDir      string   // def directory holding build.sh / test.sh
	BuildScript string   // DefDir/build.sh
	BuildArgs   []string // extra build.sh flags — the variant selector (e.g. --variant mcp)
	TestScript  string   // DefDir/test.sh (may not exist; TestPlan checks at run time)
}

// sidecars are the fixed egress-enforcement images (no harness manifest). Name
// doubles as the defs/sidecars/<name> subdir and the proveo/<name> image.
var sidecars = []string{"egress-proxy", "mitmproxy"}

var variantArgs = map[string][]string{
	"claudecode":          {"--variant", "mcp"},
	"claudecode-solidity": {"--variant", "solidity"},
	"claudecode-browser":  {"--browser"},
	"opencode-browser":    {"--browser"},
	"cursor-browser":      {"--browser"},
}

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

type Command struct {
	Dir   string
	Argv  []string
	Quiet bool
}

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
