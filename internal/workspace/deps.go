// SPEC: _spec/internal/workspace/mount-model.puml, _spec/internal/workspace/subdir-scope-mounts.puml, _spec/packages/lib/dependency-trees.puml
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/wsscan"
)

// DepLang is one row of the dependency-tree table: which files root a project of
// that language, and which directories its tooling materialises beside them.
// Kept in lockstep with _dep_lang_markers / _dep_lang_dirs in
// packages/lib/entrypoint-lib.sh; internal/contract pins the two together.
type DepLang struct {
	Lang    string
	Markers []string // filenames (filepath.Match globs) that root a project
	Dirs    []string // project-relative dirs the tooling writes; may be nested (vendor/bundle)
}

// DepLangs is the table. Languages with nothing host-specific in-tree have no
// row here, but do have one in the shell's _dep_lang_class.
var DepLangs = []DepLang{
	{Lang: "typescript", Markers: []string{"package.json"}, Dirs: []string{"node_modules"}},
	{Lang: "python", Markers: []string{"pyproject.toml", "requirements*.txt", "Pipfile", "uv.lock", "poetry.lock", "environment.yml", "environment.yaml"}, Dirs: []string{".venv", "venv"}},
	{Lang: "ruby", Markers: []string{"Gemfile"}, Dirs: []string{"vendor/bundle"}},
	{Lang: "lua", Markers: []string{"*.rockspec"}, Dirs: []string{"lua_modules"}},
	{Lang: "terraform", Markers: []string{".terraform.lock.hcl"}, Dirs: []string{".terraform"}},
	{Lang: "rust", Markers: []string{"Cargo.toml"}, Dirs: []string{"target"}},
	{Lang: "cpp", Markers: []string{"CMakeLists.txt", "meson.build"}, Dirs: []string{"build"}},
	{Lang: "zig", Markers: []string{"build.zig"}, Dirs: []string{"zig-cache", ".zig-cache", "zig-out"}},
}

// DepScanDepth bounds how deep under the scope project roots are looked for.
// Mirrors the seed's PROVEO_DEP_SCAN_DEPTH default so the trees proveo isolates
// are exactly the trees the seed then installs into.
const DepScanDepth = 4

// depScanBudget caps directory entries visited, so a pathological tree cannot
// stall the plan. Matches wsscan.DefaultBudget in spirit; the walk here prunes
// far more aggressively so it is rarely approached.
const depScanBudget = 20000

// DepCopy is one isolated dependency tree.
//
//	Host      the operator's tree; may not exist
//	Stage     the private directory proveo mounts in its place
//	Container where it lands in the agent's view
type DepCopy struct {
	Host, Stage, Container string
	Lang, Dir              string
}

// depTree is a (project root, language, dir) triple found by the walk.
type depTree struct {
	root string
	lang DepLang
	dir  string
}

// projectLangs returns the table rows whose markers match an entry of dir.
func projectLangs(entries []fs.DirEntry) []DepLang {
	var out []DepLang
	for _, l := range DepLangs {
		if markersMatch(l.Markers, entries) {
			out = append(out, l)
		}
	}
	return out
}

func markersMatch(markers []string, entries []fs.DirEntry) bool {
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		for _, m := range markers {
			if ok, _ := filepath.Match(m, e.Name()); ok {
				return true
			}
		}
	}
	return false
}

// depTreesUnder walks root to DepScanDepth and returns every tree the table
// names for a project found there, present on the host or not. Dep dirs and the
// shared prune set are never descended into.
func depTreesUnder(root string) []depTree {
	root = filepath.Clean(root)
	var out []depTree
	spent := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if spent >= depScanBudget {
			return fs.SkipAll
		}
		if p != root {
			if wsscan.AlwaysPrune(d.Name()) {
				return fs.SkipDir
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil || len(strings.Split(filepath.ToSlash(rel), "/")) > DepScanDepth {
				return fs.SkipDir
			}
		}
		entries, rerr := os.ReadDir(p)
		if rerr != nil {
			return fs.SkipDir
		}
		spent += len(entries)
		for _, l := range projectLangs(entries) {
			for _, dir := range l.Dirs {
				out = append(out, depTree{root: p, lang: l, dir: dir})
			}
		}
		return nil
	})
	return out
}

// depStage is where copies are staged. The fallback is keyed by pid, so
// concurrent runs cannot collide and repeated Plan calls in one process agree.
func (w MountSpec) depStage() string {
	if w.DepStage != "" {
		return filepath.Clean(w.DepStage)
	}
	return filepath.Join(os.TempDir(), "proveo-deps", fmt.Sprint(os.Getpid()))
}

// stagePath names a copy's staging dir from its container path. Deterministic,
// so Plan is a pure function and --print renders the argv the run will use.
func stagePath(stage, container string) string {
	sum := sha256.Sum256([]byte(container))
	return filepath.Join(stage, hex.EncodeToString(sum[:5])+"-"+filepath.Base(container))
}

// DepCopies is the PURE half of dependency isolation: which host trees the plan
// hides, and where each private copy is staged. Two sets, mirroring Plan's two
// layouts — under the scope tree every table row gets an overlay present or
// not, at the repo root of a subdir scope only trees that EXIST are copied.
func (w MountSpec) DepCopies() []DepCopy {
	scopeHost, scopeContainer := w.scope()
	if scopeHost == "" {
		return nil
	}
	stage := w.depStage()
	var out []DepCopy
	seen := map[string]bool{}
	add := func(host, container, lang, dir string) {
		if seen[container] {
			return
		}
		seen[container] = true
		out = append(out, DepCopy{Host: host, Stage: stagePath(stage, container), Container: container, Lang: lang, Dir: dir})
	}
	for _, t := range depTreesUnder(scopeHost) {
		rel := relSlash(scopeHost, t.root)
		container := scopeContainer
		if rel != "" && rel != "." {
			container += "/" + rel
		}
		add(filepath.Join(t.root, t.dir), container+"/"+t.dir, t.lang.Lang, t.dir)
	}
	if w.MountRootDeps && w.RepoRoot != "" && !sameDir(scopeHost, w.RepoRoot) && underDir(scopeHost, w.RepoRoot) {
		entries, err := os.ReadDir(w.RepoRoot)
		if err == nil {
			for _, l := range projectLangs(entries) {
				for _, dir := range l.Dirs {
					host := filepath.Join(w.RepoRoot, dir)
					if !isDir(host) || exists(filepath.Join(w.InputDir, dir)) {
						continue
					}
					add(host, "/app/"+dir, l.Lang, dir)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Container < out[j].Container })
	return out
}

// scope returns the host directory bind-mounted as the workspace and where it
// lands — the same resolution Plan performs, factored so both agree.
func (w MountSpec) scope() (host, container string) {
	switch {
	case w.RepoRoot != "" && sameDir(w.InputDir, w.RepoRoot):
		return w.RepoRoot, "/app"
	case w.RepoRoot != "" && underDir(w.InputDir, w.RepoRoot):
		return w.InputDir, "/app/" + relSlash(w.RepoRoot, w.InputDir)
	default:
		return w.InputDir, "/app"
	}
}

// depMounts renders DepCopies as the rw overlays Plan appends. Writable even
// when the workspace is read-only: an install has to land somewhere, and the
// copy is the one place it can go without touching the operator's files.
func (w MountSpec) depMounts() []runner.Mount {
	var out []runner.Mount
	for _, c := range w.DepCopies() {
		out = append(out, runner.Mount{Host: c.Stage, Container: c.Container})
	}
	return out
}

// MaterializeDeps is the WRITING half: it creates every staging dir and, when
// reuse is set, plain-copies the host tree into it. Best-effort per tree,
// returning the copies made and the joined errors.
func MaterializeDeps(copies []DepCopy, reuse bool) ([]DepCopy, error) {
	var errs []error
	var made []DepCopy
	for _, c := range copies {
		if err := os.MkdirAll(c.Stage, 0o755); err != nil {
			errs = append(errs, fmt.Errorf("stage %s: %w", c.Container, err))
			continue
		}
		if !reuse || !isDir(c.Host) {
			continue // nothing worth carrying over: the empty overlay is the isolation
		}
		if err := copyTree(c.Host, c.Stage); err != nil {
			errs = append(errs, fmt.Errorf("copy %s: %w", c.Container, err))
			continue
		}
		made = append(made, c)
	}
	return made, errors.Join(errs...)
}

// copyTree plain-copies src's contents into dst (which exists), preserving
// symlinks, modes and times. On macOS it asks for clonefile(2) first.
func copyTree(src, dst string) error {
	args := []string{"-a"}
	if runtime.GOOS == "darwin" {
		args = append(args, "-c")
	}
	// src+"/." copies the CONTENTS; filepath.Join would clean the dot away.
	args = append(args, src+"/.", dst)
	out, err := exec.Command("cp", args...).CombinedOutput()
	if err != nil && runtime.GOOS == "darwin" {
		// An older cp, or a volume that rejects -c outright: retry without it.
		out, err = exec.Command("cp", "-a", src+"/.", dst).CombinedOutput()
	}
	if err != nil {
		return fmt.Errorf("cp: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StripDepCopies removes the staged overlays from a mount list and reports how
// many it dropped. A nested overlay has no expression on the sbx backend.
func StripDepCopies(mounts []runner.Mount, stage string) ([]runner.Mount, int) {
	if stage == "" {
		return mounts, 0
	}
	stage = filepath.Clean(stage)
	var out []runner.Mount
	dropped := 0
	for _, m := range mounts {
		if underDir(m.Host, stage) {
			dropped++
			continue
		}
		out = append(out, m)
	}
	return out, dropped
}
