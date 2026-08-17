// SPEC: _spec/internal/workspace/mount-model.puml, _spec/internal/workspace/mount-symlink-escape.puml, _spec/packages/lib/steps.puml, _spec/_conventions/design-decision-ids.puml
package workspace

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/wsscan"
)

var rootFiles = []string{
	"AGENTS.md", "CONVENTIONS.md", "CLAUDE.md", ".cursorrules",
	"package.json", "pnpm-workspace.yaml", "pnpm-lock.yaml", "package-lock.json",
	"yarn.lock", "turbo.json", "nx.json", "opencode.json", "opencode.jsonc",
}

// SPEC: _spec/internal/workspace/subdir-scope-mounts.puml, _spec/internal/workspace/git-mount-by-scope.puml
var rootDirs = []string{
	"_spec",
	"vendor",
}

var rootDepDirs = []string{
	"node_modules",
}

type MountSpec struct {
	manifest.Workspace        // Layout, ConfigDir, GitMode, Output, Mode
	RepoRoot           string // git root; "" when not in a repo
	InputDir           string // invocation dir (absolute) — the monorepo scope when a subdir
	OutputDir          string
	EgressMode         string
	Credentials        string // "broker" (default) | "forward"
	MountRootDeps      bool
}

// ScopeRel returns the repo-relative scope path when only PART of the repo is
// mounted, or "" when the container sees the whole tree. The container's git
// worktree root is /app either way, so in the partial case git measures a
// whole-repo index against a directory that holds only the mounted paths and
// reports every unmounted file as deleted — see scope_git_worktree.
func (w MountSpec) ScopeRel() string {
	if w.Layout == "input-output" || w.RepoRoot == "" {
		return ""
	}
	if sameDir(w.InputDir, w.RepoRoot) || !underDir(w.InputDir, w.RepoRoot) {
		return ""
	}
	return relSlash(w.RepoRoot, w.InputDir)
}

// Plan returns the bind mounts, the container workdir, and the escaping symlinks
// it resolved (see Link) for the spec, reproducing the per-harness run.sh mount
// models. It inspects the filesystem (existence of root files / config dir /
// .env) exactly as the Bash did.
func (w MountSpec) Plan() (mounts []runner.Mount, workdir string, links []Link) {
	if w.Layout == "input-output" {
		ro := w.Mode == "ro"
		mounts := []runner.Mount{
			{Host: w.InputDir, Container: "/workspace/input", ReadOnly: ro},
			{Host: w.OutputDir, Container: "/workspace/output"},
		}
		mounts = append(mounts, w.gitOverride(w.InputDir, "/workspace/input", ro)...)
		mounts = append(mounts, w.worktreeMounts()...)
		mounts = append(mounts, w.envOverlay()...)
		// Mask .env under input when egress isolates secrets (proxy/firewall).
		if w.isolateEnv() {
			mounts = append(mounts, maskEnvMounts(w.InputDir, "/workspace/input")...)
		}
		linkMounts, links := w.linkMounts(w.InputDir, "/workspace/input", ro)
		return append(mounts, linkMounts...), "", links
	}

	ro := w.Mode == "ro"
	gitRO := w.GitMode == "ro"
	scopeHost, scopeContainer := w.InputDir, "/app"
	switch {
	case w.RepoRoot != "" && sameDir(w.InputDir, w.RepoRoot):
		mounts = append(mounts, runner.Mount{Host: w.RepoRoot, Container: "/app", ReadOnly: ro})
		mounts = append(mounts, w.gitOverride(w.RepoRoot, "/app", ro)...)
		mounts = append(mounts, w.envMounts("")...)
		scopeHost = w.RepoRoot
	case w.RepoRoot != "" && underDir(w.InputDir, w.RepoRoot):
		rel := relSlash(w.RepoRoot, w.InputDir)
		mounts = append(mounts,
			runner.Mount{Host: w.InputDir, Container: "/app/" + rel, ReadOnly: ro},
			runner.Mount{Host: filepath.Join(w.RepoRoot, ".git"), Container: "/app/.git", ReadOnly: gitRO},
		)
		for _, f := range rootFiles {
			if exists(filepath.Join(w.RepoRoot, f)) && !exists(filepath.Join(w.InputDir, f)) {
				mounts = append(mounts, runner.Mount{Host: filepath.Join(w.RepoRoot, f), Container: "/app/" + f, ReadOnly: true})
			}
		}
		dirs := rootDirs
		if w.MountRootDeps {
			dirs = append(append([]string{}, rootDirs...), rootDepDirs...)
		}
		for _, d := range dirs {
			host := filepath.Join(w.RepoRoot, d)
			if !isDir(host) || exists(filepath.Join(w.InputDir, d)) {
				continue
			}
			mounts = append(mounts, runner.Mount{Host: host, Container: "/app/" + d})
			if w.isolateEnv() && !envMaskPrune[d] {
				mounts = append(mounts, maskEnvMounts(host, "/app/"+d)...)
			}
		}
		if w.ConfigDir != "" && exists(filepath.Join(w.RepoRoot, w.ConfigDir)) && !exists(filepath.Join(w.InputDir, w.ConfigDir)) {
			mounts = append(mounts, runner.Mount{Host: filepath.Join(w.RepoRoot, w.ConfigDir), Container: "/app/" + w.ConfigDir, ReadOnly: true})
		}
		mounts = append(mounts, w.envMounts(rel)...)
		scopeContainer = "/app/" + rel
	default: // not a repo
		mounts = append(mounts, runner.Mount{Host: w.InputDir, Container: "/app", ReadOnly: ro})
		mounts = append(mounts, w.envMounts("")...)
	}
	mounts = append(mounts, w.worktreeMounts()...)
	mounts = append(mounts, w.envOverlay()...)
	if w.Output && w.OutputDir != "" {
		mounts = append(mounts, runner.Mount{Host: w.OutputDir, Container: "/app/output"})
	}
	linkMounts, links := w.linkMounts(scopeHost, scopeContainer, ro)
	return append(mounts, linkMounts...), "/app", links
}

// resolved returns p with symlinks expanded, or p unchanged when it cannot be.
// RepoRoot comes from `git rev-parse`, which always reports the REAL path, while
// InputDir is whatever the caller passed. On macOS those differ for anything
// under /tmp or /var (→ /private/…), and for any symlinked project dir on every
// platform — so comparing them literally makes a real repo look like "not a
// repo", which drops .git from the plan entirely.
func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	return filepath.Clean(resolved(a)) == filepath.Clean(resolved(b))
}

func underDir(path, root string) bool {
	if hasPrefixDir(filepath.Clean(path), filepath.Clean(root)) {
		return true
	}
	return hasPrefixDir(filepath.Clean(resolved(path)), filepath.Clean(resolved(root)))
}

func hasPrefixDir(path, root string) bool {
	return strings.HasPrefix(path+string(filepath.Separator), root+string(filepath.Separator))
}

func relSlash(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	if rel, err := filepath.Rel(resolved(root), resolved(path)); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.Base(path)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func isDir(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }

// ContainerGitCommonDir is where a linked worktree's shared .git is mounted.
const ContainerGitCommonDir = "/proveo-git"

// gitWorktree is a LINKED worktree: .git is a FILE holding "gitdir: <abs path>"
// that points under the MAIN repo, outside the mounted tree. Nothing follows
// that pointer into the container, so git reports "not a git repository" and
// every downstream signal (identity, gh, verify) degrades with it.
type gitWorktree struct {
	CommonDir string // <main>/.git — objects, refs, config
	Name      string // per-worktree dir under <common>/worktrees/
}

// readGitWorktree parses the pointer chain from the filesystem alone, matching
// how the rest of Plan works (no git binary, no shelling out).
func readGitWorktree(tree string) (gitWorktree, bool) {
	b, err := os.ReadFile(filepath.Join(tree, ".git"))
	if err != nil {
		return gitWorktree{}, false // a directory (normal repo) or absent
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return gitWorktree{}, false
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(tree, gitDir)
	}
	// commondir is written relative to the per-worktree dir.
	cb, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitWorktree{}, false
	}
	common := strings.TrimSpace(string(cb))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	if !isDir(common) {
		return gitWorktree{}, false
	}
	return gitWorktree{CommonDir: filepath.Clean(common), Name: filepath.Base(gitDir)}, true
}

// WorktreeEnv returns GIT_DIR/GIT_WORK_TREE for a linked worktree, or nil. The
// per-worktree commondir file is RELATIVE, so pointing GIT_DIR inside the
// mounted common dir resolves without host and container paths having to match.
func (w MountSpec) WorktreeEnv() []string {
	wt, ok := readGitWorktree(w.InputDir)
	if !ok {
		return nil
	}
	return []string{
		"GIT_DIR=" + ContainerGitCommonDir + "/worktrees/" + wt.Name,
		"GIT_WORK_TREE=" + w.containerRoot(),
	}
}

func (w MountSpec) containerRoot() string {
	if w.Layout == "input-output" {
		return "/workspace/input"
	}
	return "/app"
}

// worktreeMounts carries the shared .git of a linked worktree into the container.
func (w MountSpec) worktreeMounts() []runner.Mount {
	wt, ok := readGitWorktree(w.InputDir)
	if !ok {
		return nil
	}
	return []runner.Mount{{Host: wt.CommonDir, Container: ContainerGitCommonDir}}
}

// envOverlay binds the workspace .env when it resolves on the host but would not
// inside the container — the usual cause being a symlink to a path outside the
// mounted tree (a worktree pointing back at its main checkout). Docker resolves
// the host path, so binding the link lands the real file. Never in isolate mode,
// where .env is deliberately masked instead.
func (w MountSpec) envOverlay() []runner.Mount {
	if w.isolateEnv() {
		return nil
	}
	src := filepath.Join(w.InputDir, ".env")
	fi, err := os.Lstat(src)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return nil // absent, or a real file already inside the mount
	}
	target, err := filepath.EvalSymlinks(src)
	if err != nil || underDir(target, w.InputDir) {
		return nil // dangling on the host too, or resolves within the mount
	}
	return []runner.Mount{{Host: target, Container: w.containerRoot() + "/.env", ReadOnly: true}}
}

// gitOverride pins .git's read-only state when it differs from the parent mount
// it sits inside, by layering a nested bind over that path. Empty when the
// parent already yields the requested state or the tree carries no .git.
func (w MountSpec) gitOverride(hostTree, containerBase string, parentRO bool) []runner.Mount {
	gitRO := w.GitMode == "ro"
	if gitRO == parentRO {
		return nil
	}
	host := filepath.Join(hostTree, ".git")
	if !isDir(host) {
		return nil
	}
	return []runner.Mount{{Host: host, Container: containerBase + "/.git", ReadOnly: gitRO}}
}

func (w MountSpec) isolateEnv() bool {
	if strings.EqualFold(strings.TrimSpace(w.Credentials), "forward") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(w.EgressMode)) {
	case "open", "allowlist", "review", "broker", "proxy", "firewall":
		return true
	}
	return false
}

func (w MountSpec) envMounts(relativeScope string) []runner.Mount {
	if w.isolateEnv() {
		base := "/app"
		if relativeScope != "" {
			base += "/" + relativeScope
		}
		return maskEnvMounts(w.InputDir, base)
	}
	if env := envMountSource(w.InputDir, w.RepoRoot); env != "" {
		return []runner.Mount{{Host: env, Container: "/app/.env", ReadOnly: true}}
	}
	return nil
}

// envMaskPrune are directories skipped when hunting for dotenv files to mask:
// huge and never the project's own secrets.
var envMaskPrune = map[string]bool{".git": true, "node_modules": true}

func secretEnvFile(name string) bool {
	if name != ".env" && !strings.HasPrefix(name, ".env.") {
		return false
	}
	switch {
	case strings.HasSuffix(name, ".example"), strings.HasSuffix(name, ".sample"),
		strings.HasSuffix(name, ".template"), strings.HasSuffix(name, ".dist"):
		return false
	}
	return true
}

func maskEnvMounts(hostDir, containerBase string) []runner.Mount {
	if hostDir == "" {
		return nil
	}
	root := filepath.Clean(hostDir)
	var masks []runner.Mount
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip it, never abort the plan
		}
		if d.IsDir() {
			if p != root && envMaskPrune[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !secretEnvFile(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		masks = append(masks, runner.Mount{
			Host:      "/dev/null",
			Container: containerBase + "/" + filepath.ToSlash(rel),
			ReadOnly:  true,
		})
		return nil
	})
	return masks
}

type LinkAction string

const (
	LinkMounted   LinkAction = "mounted"
	LinkRefused   LinkAction = "refused"
	LinkEnvPolicy LinkAction = "env-policy"
)

type Link struct {
	Rel    string
	Target string
	Action LinkAction
	Reason string
}

const (
	linkScanDepth = 3
	linkMountCap  = 32
)

func (w MountSpec) linkMounts(hostDir, containerBase string, ro bool) ([]runner.Mount, []Link) {
	if hostDir == "" {
		return nil, nil
	}
	root := filepath.Clean(hostDir)
	var mounts []runner.Mount
	var links []Link
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		depth := len(strings.Split(filepath.ToSlash(rel), "/"))
		if d.IsDir() {
			if wsscan.AlwaysPrune(d.Name()) || depth >= linkScanDepth {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		link := Link{Rel: filepath.ToSlash(rel)}
		if secretEnvFile(d.Name()) {
			link.Action, link.Reason = LinkEnvPolicy, "placed by the credential policy, not by link resolution"
			links = append(links, link)
			return nil
		}
		target, terr := filepath.EvalSymlinks(p)
		if terr != nil {
			link.Action, link.Reason = LinkRefused, "target does not resolve on the host either"
			links = append(links, link)
			return nil
		}
		if sameDir(target, root) || underDir(target, root) {
			return nil
		}
		link.Target = target
		switch {
		case refuseLinkTarget(target, root) != "":
			link.Action, link.Reason = LinkRefused, refuseLinkTarget(target, root)
		case len(mounts) >= linkMountCap:
			link.Action, link.Reason = LinkRefused, fmt.Sprintf("more than %d escaping symlinks in this tree", linkMountCap)
		default:
			mounts = append(mounts, runner.Mount{
				Host: target, Container: containerBase + "/" + filepath.ToSlash(rel), ReadOnly: ro,
			})
			link.Action = LinkMounted
		}
		links = append(links, link)
		return nil
	})
	return mounts, links
}

func refuseLinkTarget(target, root string) string {
	if clean := filepath.Clean(target); clean == string(filepath.Separator) || underDir(root, clean) {
		return "target contains the workspace"
	}
	if len(strings.Split(strings.Trim(filepath.ToSlash(filepath.Clean(target)), "/"), "/")) < 2 {
		return "target is a top-level system directory"
	}
	return ""
}

func envMountSource(inputDir, repoRoot string) string {
	candidates := []string{filepath.Join(inputDir, ".env")}
	if repoRoot != "" {
		candidates = append(candidates, filepath.Join(repoRoot, ".env"))
	}
	for _, candidate := range candidates {
		if resolved := resolveRegularFile(candidate); resolved != "" {
			return resolved
		}
	}
	return ""
}

func resolveRegularFile(path string) string {
	if _, err := os.Lstat(path); err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return resolved
	}
	return abs
}

func EnvFileSource(invocationWD, inputDir, repoRoot string) string {
	if invocationWD != "" {
		if p := resolveRegularFile(filepath.Join(invocationWD, ".env")); p != "" {
			return p
		}
	}
	if p := envMountSource(inputDir, repoRoot); p != "" {
		return p
	}
	for _, dir := range []string{inputDir, invocationWD} {
		if dir == "" {
			continue
		}
		if p := findEnvFileResolved(dir); p != "" {
			return p
		}
	}
	return ""
}

func findEnvFileResolved(dir string) string {
	p := entrypoint.FindEnvFile(dir)
	if p == "" {
		return ""
	}
	if resolved := resolveRegularFile(p); resolved != "" {
		return resolved
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}
