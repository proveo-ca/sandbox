// SPEC: _spec/internal/maintain/image-build-deploy.puml, _spec/_devops/image-lineage-and-publish.puml
package maintain

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/imagebuild"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/ui"
)

const (
	KindBase    = "base"
	KindHarness = "harness"
	KindSidecar = "sidecar"
)

// Target is one buildable/deployable image in the maintainer registry.
type Target struct {
	Name       string // e.g. "claudecode-solidity"
	Kind       string // KindBase | KindHarness | KindSidecar
	Image      string // org/name without a tag, e.g. "proveo/claudecode-solidity"
	DefDir     string // def directory holding the Dockerfile / test.sh
	RepoRoot   string // build context root
	TestScript string // DefDir/test.sh (may not exist; TestPlan checks at run time)
}

var sidecars = []string{"egress-proxy", "mitmproxy"}

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

	for i := range out {
		out[i].RepoRoot = filepath.Dir(defsDir)
		out[i].TestScript = filepath.Join(out[i].DefDir, "test.sh")
	}
	return out
}

// Wave is one Schedule execute unit.
type Wave struct {
	Concurrent bool
	Targets    []Target
}

func (w Wave) Names() []string {
	out := make([]string, len(w.Targets))
	for i, t := range w.Targets {
		out[i] = t.Name
	}
	return out
}

func parentOf(name string) string {
	switch name {
	case "base-node":
		return "base"
	case "base-node-lsp":
		return "base-node"
	case "base-node-browser":
		return "base-node-lsp"
	case "claudecode-solidity", "claudecode-browser":
		return "claudecode"
	case "codex-browser":
		return "codex"
	case "opencode-browser":
		return "opencode"
	case "cursor-browser":
		return "cursor"
	default:
		return ""
	}
}

func Schedule(ts []Target) []Wave {
	var bases, rest []Target
	for _, t := range ts {
		if t.Kind == KindBase {
			bases = append(bases, t)
		} else {
			rest = append(rest, t)
		}
	}
	var waves []Wave
	for _, b := range bases {
		waves = append(waves, Wave{Targets: []Target{b}})
	}
	remaining := rest
	for len(remaining) > 0 {
		pending := map[string]bool{}
		for _, t := range remaining {
			pending[t.Name] = true
		}
		var ready, blocked []Target
		for _, t := range remaining {
			if p := parentOf(t.Name); p != "" && pending[p] {
				blocked = append(blocked, t)
				continue
			}
			ready = append(ready, t)
		}
		if len(ready) == 0 {
			ready = remaining
			blocked = nil
		}
		waves = append(waves, Wave{Concurrent: len(ready) > 1, Targets: ready})
		remaining = blocked
	}
	return waves
}

func FormatWaveHeader(w Wave) string {
	names := strings.Join(w.Names(), " ")
	if w.Concurrent {
		return "# concurrent " + names
	}
	return "# serial " + names
}

func FormatTargetElapsed(name string, d *time.Duration) (string, error) {
	if d == nil {
		return "", fmt.Errorf("missing duration for %s", name)
	}
	return fmt.Sprintf("%s in %s", name, d.Round(time.Millisecond)), nil
}

func FormatRunSummary(verb string, n int, wall *time.Duration) (string, error) {
	if wall == nil {
		return "", fmt.Errorf("%s summary missing duration", verb)
	}
	return fmt.Sprintf("%s %d target(s) in %s", verb, n, wall.Round(time.Millisecond)), nil
}

type Command struct {
	Dir   string
	Argv  []string
	Quiet bool
	Run   func(stdout, stderr io.Writer, env []string) error // in-process step; Argv is its label
}

// LocalTag is the only tag a --load build ever writes, and it is never pushed.
const (
	LocalTag   = "local"
	PublishTag = "latest"
)

func (t Target) BuildPlan(tag string, noCache bool) []Command {
	tag = normTag(tag, LocalTag)
	return []Command{
		t.imageBuild(tag, imagebuild.Options{NoCache: noCache}),
		{Argv: []string{"docker", "image", "inspect", t.Image + ":" + tag}, Quiet: true},
	}
}

// DeployPlan promotes the tested local build and publishes it.
func (t Target) DeployPlan(tag string) []Command {
	tag = normTag(tag, PublishTag)
	local := t.Image + ":" + LocalTag
	return []Command{
		{Argv: []string{"docker", "image", "inspect", local}, Quiet: true},
		{Argv: []string{"docker", "tag", local, t.Image + ":" + tag}, Quiet: true},
		t.imageBuild(tag, imagebuild.Options{Push: true}),
	}
}

func (t Target) imageBuild(tag string, o imagebuild.Options) Command {
	argv := []string{"imagebuild", t.Name, "--tag", tag}
	if o.Push {
		argv = append(argv, "--push")
	}
	if o.NoCache {
		argv = append(argv, "--no-cache")
	}
	return Command{Argv: argv, Run: func(stdout, stderr io.Writer, env []string) error {
		b := imagebuild.New(t.RepoRoot)
		b.Out, b.UI, b.Getenv = stdout, ui.New(stderr), overlayEnv(env)
		return b.BuildTarget(t.Name, tag, o)
	}}
}

func overlayEnv(env []string) func(string) string {
	over := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			over[k] = v
		}
	}
	return func(k string) string {
		if v, ok := over[k]; ok {
			return v
		}
		return os.Getenv(k)
	}
}

// TestPlan runs the def's test.sh.
func (t Target) TestPlan(exists func(string) bool) []Command {
	if t.TestScript == "" || !exists(t.TestScript) {
		return nil
	}
	return []Command{{Dir: t.DefDir, Argv: []string{"bash", t.TestScript}}}
}

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
