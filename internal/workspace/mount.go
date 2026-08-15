package workspace

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/runner"
)

// rootFiles are workspace-shared files preserved (read-only) from the repo root
// into a monorepo-subdir `/app` mount. A superset across harnesses; each is
// mounted only if it exists at the root and not already in the scope dir — so
// the union is safe (a harness never sees a file its repo doesn't have).
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

// MountSpec is the resolved input to mount planning: the manifest's mount model
// (embedded — the single source of that shape, D5) plus the concrete paths for
// this run. It lives beside the git-scope resolver here, not in runner, which
// stays a pure argv formatter (D4).
type MountSpec struct {
	manifest.Workspace        // Layout, ConfigDir, GitMode, Output, Mode
	RepoRoot           string // git root; "" when not in a repo
	InputDir           string // invocation dir (absolute) — the monorepo scope when a subdir
	OutputDir          string
	// (recursively, both layouts) with /dev/null, so a hostile/injected agent can't
	// read a real credential off disk — the structural complement to the broker
	// header-strip + egress DLP. Templates (.env.example/.sample/.template/.dist)
	// stay readable.
	EgressMode    string
	Credentials   string // "broker" (default) | "forward"
	MountRootDeps bool
}

// Plan returns the bind mounts and container workdir for the spec, reproducing
// the per-harness run.sh mount models. It inspects the filesystem (existence of
// root files / config dir / .env) exactly as the Bash did.
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

func (w MountSpec) Plan() (mounts []runner.Mount, workdir string) {
	if w.Layout == "input-output" {
		ro := w.Mode == "ro"
		mounts := []runner.Mount{
			{Host: w.InputDir, Container: "/workspace/input", ReadOnly: ro},
			{Host: w.OutputDir, Container: "/workspace/output"},
		}
		mounts = append(mounts, w.gitOverride(w.InputDir, "/workspace/input", ro)...)
		// Mask .env under input when egress isolates secrets (proxy/firewall).
		if w.isolateEnv() {
			mounts = append(mounts, maskEnvMounts(w.InputDir, "/workspace/input")...)
		}
		return mounts, ""
	}

	ro := w.Mode == "ro"
	gitRO := w.GitMode == "ro"
	switch {
	case w.RepoRoot != "" && sameDir(w.InputDir, w.RepoRoot):
		mounts = append(mounts, runner.Mount{Host: w.RepoRoot, Container: "/app", ReadOnly: ro})
		mounts = append(mounts, w.gitOverride(w.RepoRoot, "/app", ro)...)
		mounts = append(mounts, w.envMounts("")...)
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
	default: // not a repo
		mounts = append(mounts, runner.Mount{Host: w.InputDir, Container: "/app", ReadOnly: ro})
		mounts = append(mounts, w.envMounts("")...)
	}
	if w.Output && w.OutputDir != "" {
		mounts = append(mounts, runner.Mount{Host: w.OutputDir, Container: "/app/output"})
	}
	return mounts, "/app"
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

// envMounts returns .env-related mounts for the app-layout tree (host = InputDir,
// mounted at containerBase = /app[/<relativeScope>]). In broker mode it overlays
// the resolved host .env at /app/.env. In proxy/firewall it masks every dotenv
// secrets file under the mounted tree with /dev/null so a hostile/injected agent
// can't read a real credential off disk — the structural complement to the broker
// header-strip and the egress DLP (see internal/broker, internal/egresspolicy).
//
// The separately-mounted repo-root files (rootFiles) and configDir are not walked:
// rootFiles is a fixed non-secret allowlist, and configDir is a tool-config dir —
// neither is a conventional secrets location.
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

// secretEnvFile reports whether basename is a dotenv secrets file that must not be
// readable inside the agent. Matches ".env" and ".env.*" but leaves the
// conventional non-secret templates readable (agents legitimately consult them).
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

// maskEnvMounts walks hostDir (pruning .git/node_modules; WalkDir does not follow
// symlinks, so no loops and a symlinked .env is still masked at its container
// path) and returns a /dev/null:ro mask for every dotenv secrets file, at its
// path under containerBase. Best-effort by design — a read error on any entry is
// skipped rather than aborting the run.
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

// resolveRegularFile returns the absolute path of a regular file, following
// symlinks on the host. Used for .env overlays when the project symlink points
// outside the bind-mounted tree.
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

// EnvFileSource returns a host-side .env path for broker ingestion (never for
// agent mounts in proxy/firewall). Matches the legacy egress.sh order:
// invocationWD (host PWD) first, then scope inputDir / repoRoot, then
// proveo-entrypoint's git-root / walk-up search.
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
