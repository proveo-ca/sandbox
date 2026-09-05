// SPEC: _spec/internal/workspace/mount-model.puml,
// _spec/internal/workspace/mount-symlink-escape.puml,
// _spec/internal/workspace/worktree-git-linkage.puml,
// _spec/packages/lib/steps.puml, _spec/_conventions/design-decision-ids.puml
//
// SPEC: _spec/internal/workspace/mount-model.puml, _spec/internal/workspace/mount-symlink-escape.puml, _spec/internal/workspace/worktree-git-linkage.puml, _spec/packages/lib/steps.puml, _spec/_conventions/design-decision-ids.puml
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
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

type MountSpec struct {
	manifest.Workspace        // Layout, ConfigDir, GitMode, Output, Mode
	RepoRoot           string // git root; "" when not in a repo
	InputDir           string // invocation dir (absolute) — the monorepo scope when a subdir
	OutputDir          string
	EgressMode         string
	Credentials        string // "broker" (default) | "forward"
	MountRootDeps      bool
	DepStage           string
	WorktreeLinkDir    string
}

// ScopeRel returns the repo-relative scope path when only PART of the repo is
// mounted, or "" when the container sees the whole tree.
func (w MountSpec) ScopeRel() string {
	if w.RepoRoot == "" {
		return ""
	}
	if sameDir(w.InputDir, w.RepoRoot) || !underDir(w.InputDir, w.RepoRoot) {
		return ""
	}
	return relSlash(w.RepoRoot, w.InputDir)
}

// Plan returns the bind mounts, the container workdir, and the escaping
// symlinks it resolved (see Link) for the spec, reproducing the per-harness
// run.sh mount models.
func (w MountSpec) Plan() (mounts []runner.Mount, workdir string, links []Link) {
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
	// SPEC: _spec/packages/lib/dependency-trees.puml
	mounts = append(mounts, w.depMounts()...)

	mounts = append(mounts, w.worktreeMounts()...)
	mounts = append(mounts, w.envOverlay()...)
	if w.Output && w.OutputDir != "" {
		mounts = append(mounts, runner.Mount{Host: w.OutputDir, Container: "/app/output"})
	}
	linkMounts, links := w.linkMounts(scopeHost, scopeContainer, ro)
	return append(mounts, linkMounts...), "/app", links
}

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

type gitWorktree struct {
	CommonDir string // <main>/.git — objects, refs, config
	Name      string // per-worktree dir under <common>/worktrees/
}

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

func LinkedWorktree(dir string) bool {
	_, ok := readGitWorktree(dir)
	return ok
}

// WorktreeEnv returns GIT_DIR/GIT_WORK_TREE for a linked worktree, or nil.
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

func (w MountSpec) containerRoot() string { return "/app" }

func (w MountSpec) worktreeMounts() []runner.Mount {
	wt, ok := readGitWorktree(w.InputDir)
	if !ok {
		return nil
	}
	mounts := []runner.Mount{{
		Host: wt.CommonDir, Container: ContainerGitCommonDir, ReadOnly: w.GitMode == "ro",
	}}
	if w.WorktreeLinkDir == "" {
		return mounts
	}
	return append(mounts,
		runner.Mount{
			Host:      filepath.Join(w.WorktreeLinkDir, worktreeDotGitFile),
			Container: w.containerRoot() + "/.git",
			ReadOnly:  true,
		},
		runner.Mount{
			Host:      filepath.Join(w.WorktreeLinkDir, worktreeGitDirFile),
			Container: ContainerGitCommonDir + "/worktrees/" + wt.Name + "/gitdir",
			ReadOnly:  true,
		},
	)
}

const (
	worktreeDotGitFile = "dotgit" // replaces <tree>/.git
	worktreeGitDirFile = "gitdir" // replaces <common>/worktrees/<name>/gitdir
)

// PrepareWorktreeLinks materialises the two container-only pointer files for a
// linked worktree under root and returns the directory holding them, for the
// caller to assign to WorktreeLinkDir.
func (w MountSpec) PrepareWorktreeLinks(root string) (string, error) {
	wt, ok := readGitWorktree(w.InputDir)
	if !ok || root == "" {
		return "", nil
	}
	sum := sha256.Sum256([]byte(wt.CommonDir + "\x00" + wt.Name))
	dir := filepath.Join(root, "worktrees", wt.Name+"-"+hex.EncodeToString(sum[:6]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("worktree links: mkdir %s: %w", dir, err)
	}
	files := map[string]string{
		worktreeDotGitFile: "gitdir: " + ContainerGitCommonDir + "/worktrees/" + wt.Name + "\n",
		worktreeGitDirFile: w.containerRoot() + "/.git\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return "", fmt.Errorf("worktree links: write %s: %w", path, err)
		}
	}
	return dir, nil
}

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
	case "open", "allowlist", "review":
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
