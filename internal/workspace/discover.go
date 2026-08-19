// SPEC: _spec/internal/workspace/mount-model.puml
package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Project is a discovered monorepo member the CLI can offer to open.
type Project struct {
	Name string // directory basename
	Path string // repo-relative path (forward slashes)
	Tool string // "pnpm" | "npm/yarn" | "convention"
}

// markerFiles identify a directory as a real project (not just a glob match).
var markerFiles = []string{"package.json", "project.json", "Cargo.toml", "go.mod"}

// conventionGlobs are the fallback layout when no workspace manifest declares
// members — the same defaults the old registry encoded.
var conventionGlobs = []string{"apps/*", "packages/*", "libs/*", "projects/*", "services/*"}

func DiscoverProjects(root string) []Project {
	patterns, tool := workspacePatterns(root)

	seen := map[string]bool{}
	var out []Project
	for _, pat := range patterns {
		// Ignore negation patterns (pnpm/npm exclusions) as include sources.
		if strings.HasPrefix(pat, "!") {
			continue
		}
		// Normalize `**` (recursive) to a single-level glob — covers the common
		// `packages/**` while staying filepath.Glob-compatible.
		glob := strings.ReplaceAll(pat, "**", "*")
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(glob)))
		if err != nil {
			continue
		}
		for _, m := range matches {
			fi, err := os.Stat(m)
			if err != nil || !fi.IsDir() || !hasMarker(m) {
				continue
			}
			rel, err := filepath.Rel(root, m)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if seen[rel] {
				continue
			}
			seen[rel] = true
			out = append(out, Project{Name: filepath.Base(rel), Path: rel, Tool: tool})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func workspacePatterns(root string) (patterns []string, tool string) {
	if pw := pnpmPackages(root); len(pw) > 0 {
		return pw, "pnpm"
	}
	if ws := packageJSONWorkspaces(root); len(ws) > 0 {
		return ws, "npm/yarn"
	}
	return conventionGlobs, "convention"
}

func hasMarker(dir string) bool {
	for _, m := range markerFiles {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}
	return false
}

func pnpmPackages(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, "pnpm-workspace.yaml"))
	if err != nil {
		return nil
	}
	var doc struct {
		Packages []string `yaml:"packages"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	return doc.Packages
}

// packageJSONWorkspaces reads the root package.json `workspaces`, which may be
// an array of globs or an object {"packages": [...]}.
func packageJSONWorkspaces(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if err := json.Unmarshal(data, &doc); err != nil || len(doc.Workspaces) == 0 {
		return nil
	}
	var arr []string
	if err := json.Unmarshal(doc.Workspaces, &arr); err == nil {
		return arr
	}
	var obj struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(doc.Workspaces, &obj); err == nil {
		return obj.Packages
	}
	return nil
}
