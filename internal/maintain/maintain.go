// Plans are pure data ([]Command); cmd/proveo executes or prints them.
//
// SPEC: _spec/internal/maintain/image-build-deploy.puml, _spec/_devops/image-lineage-and-publish.puml
package maintain

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	"codex-browser":       {"--browser"},
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

// LocalTag is the only tag a --load build ever writes, and it is never pushed.
// PublishTag is the only tag that ever means "published".
//
// They are separate because they used to be the same: a local build and the
// registry artifact both answered to :latest, so any tool that re-resolved the
// reference — sbx pulls it at sandbox creation — could serve a week-old published
// image over the build under test, with nothing anywhere saying which one ran.
const (
	LocalTag   = "local"
	PublishTag = "latest"
)

func (t Target) BuildPlan(tag string, noCache bool) []Command {
	tag = normTag(tag, LocalTag)
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

// DeployPlan promotes the tested local build and publishes it. It REQUIRES
// <image>:local: deploying without one would publish an image nothing ran against,
// which is the whole reason build and publish stopped sharing a tag.
//
// The retag makes the local :latest identical to what was tested. The push then
// rebuilds for every platform in PROVEO_PLATFORMS, because a --load build is
// single-arch and cannot itself be published as a multi-arch manifest — layers come
// from the same cache, so the host-arch image is the tested one and the other arch
// is the same source built alongside it.
func (t Target) DeployPlan(tag string) []Command {
	tag = normTag(tag, PublishTag)
	build := append([]string{"bash", t.BuildScript}, t.BuildArgs...)
	build = append(build, "--tag", tag, "--push")
	local := t.Image + ":" + LocalTag
	return []Command{
		{Argv: []string{"docker", "image", "inspect", local}, Quiet: true},
		{Argv: []string{"docker", "tag", local, t.Image + ":" + tag}, Quiet: true},
		{Dir: t.DefDir, Argv: build},
	}
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

func normTag(tag, def string) string {
	if strings.TrimSpace(tag) == "" {
		return def
	}
	return tag
}

// ResolveImage picks between a published reference and the local build of the same
// repository, preferring whichever was built more recently.
//
// Recency rather than mere existence: a stale :local left over from last week must
// not shadow an image just pulled, and a build from a minute ago must not lose to a
// published one. Only :latest references are considered — an explicit :v2 or a
// digest is a deliberate choice and is returned untouched.
//
// created reports an image's build time, and false when the host has no such image.
func ResolveImage(ref string, created func(string) (time.Time, bool)) (chosen string, isLocal bool) {
	repo := stripTag(ref)
	if tag := RefTag(ref); tag != PublishTag {
		return ref, tag == LocalTag
	}
	localAt, haveLocal := created(repo + ":" + LocalTag)
	if !haveLocal {
		return ref, false
	}
	publishedAt, havePublished := created(ref)
	if !havePublished || localAt.After(publishedAt) {
		return repo + ":" + LocalTag, true
	}
	return ref, false
}

// RefTag returns a reference's tag, defaulting to PublishTag when it carries none.
// A digest reference has no tag and is never rewritten.
func RefTag(ref string) string {
	last := ref
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		last = ref[i+1:]
	}
	if strings.Contains(last, "@") {
		return ""
	}
	if i := strings.IndexByte(last, ':'); i >= 0 {
		return last[i+1:]
	}
	return PublishTag
}
