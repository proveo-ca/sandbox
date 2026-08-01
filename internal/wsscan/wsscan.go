// Package wsscan is the one workspace walker: it finds marker files under a
// scope without descending into dependency trees, so both the DinD gate and the
// choice prompt's header see the same monorepo-aware answer.
//
// Pruning is belt-and-braces on purpose. Deriving it from .gitignore alone is
// not safe: gitignore lookup stops at the FIRST file found walking up, so a
// monorepo scope with its own .gitignore never sees the root's node_modules
// entry; pattern forms containing / * ? [ are unusable as directory names; and a
// repo relying on core.excludesFile supplies nothing at all. So a hardcoded deny
// set always applies, and gitignore names from both the scope and the repo root
// are added on top.
//
// SPEC: _spec/_plans/harness-choice-cache.puml
package wsscan

import (
	"os"
	"path/filepath"
	"strings"
)

// MaxDepth is how deep a scan walks below the scope root.
const MaxDepth = 7

// DefaultBudget caps how many directory entries a scan examines. The scan runs
// before the container exists, in front of an operator waiting at a prompt, so a
// pathological tree must degrade rather than stall.
const DefaultBudget = 20000

// alwaysPrune are directory names never worth walking, regardless of what any
// .gitignore says. Dependency and build trees: large, and full of other
// projects' marker files (an npm package's Dockerfile is not the workspace's).
var alwaysPrune = map[string]bool{
	"node_modules": true, ".git": true, "vendor": true, "target": true,
	"dist": true, "build": true, ".venv": true, "venv": true,
	"__pycache__": true, ".next": true, ".turbo": true, ".nx": true,
	".cache": true, ".gradle": true, ".tox": true, ".mypy_cache": true,
	".pytest_cache": true, "Pods": true, ".terraform": true,
}

// Marker is one thing worth finding. Names match a basename exactly; Suffixes
// match the end of a basename (".go"). A marker is satisfied by either.
type Marker struct {
	Label    string
	Names    []string
	Suffixes []string
}

// Result reports which markers were found. Truncated is true when the budget ran
// out before the walk finished, so the answer may be incomplete.
type Result struct {
	Found     map[string]bool
	Truncated bool
}

// Has reports whether a label was found.
func (r Result) Has(label string) bool { return r.Found[label] }

// Labels returns the found labels in the order the markers were declared.
func (r Result) Labels(markers []Marker) []string {
	var out []string
	for _, m := range markers {
		if r.Found[m.Label] {
			out = append(out, m.Label)
		}
	}
	return out
}

// Scan walks scopeDir (bounded by MaxDepth and budget) for the given markers.
// repoRoot, when non-empty, contributes its .gitignore to the prune set — the
// case a scope-local .gitignore would otherwise shadow. budget <= 0 uses
// DefaultBudget.
func Scan(scopeDir, repoRoot string, markers []Marker, budget int) Result {
	res := Result{Found: map[string]bool{}}
	if scopeDir == "" {
		return res
	}
	if info, err := os.Stat(scopeDir); err != nil || !info.IsDir() {
		return res
	}
	if budget <= 0 {
		budget = DefaultBudget
	}

	prune := map[string]bool{}
	for k := range alwaysPrune {
		prune[k] = true
	}
	for _, dir := range []string{scopeDir, repoRoot} {
		for k := range gitignoreNames(dir) {
			prune[k] = true
		}
	}

	spent := 0
	walk(scopeDir, 0, markers, prune, &res, &spent, budget)
	return res
}

func walk(dir string, depth int, markers []Marker, prune map[string]bool, res *Result, spent *int, budget int) {
	if depth > MaxDepth || allFound(markers, res) {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var subdirs []string
	for _, e := range entries {
		if *spent >= budget {
			res.Truncated = true
			return
		}
		*spent++
		name := e.Name()
		if e.IsDir() {
			if !prune[name] {
				subdirs = append(subdirs, name)
			}
			continue
		}
		for _, m := range markers {
			if res.Found[m.Label] {
				continue
			}
			if matches(name, m) {
				res.Found[m.Label] = true
			}
		}
	}
	// Files at every level before descending, so a shallow hit ends the walk early.
	for _, sd := range subdirs {
		if allFound(markers, res) || *spent >= budget {
			if *spent >= budget {
				res.Truncated = true
			}
			return
		}
		walk(filepath.Join(dir, sd), depth+1, markers, prune, res, spent, budget)
	}
}

func matches(name string, m Marker) bool {
	for _, n := range m.Names {
		if name == n {
			return true
		}
	}
	for _, s := range m.Suffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

func allFound(markers []Marker, res *Result) bool {
	for _, m := range markers {
		if !res.Found[m.Label] {
			return false
		}
	}
	return true
}

// gitignoreNames returns the plain directory names listed in dir's .gitignore.
// Patterns containing / * ? [ are skipped: they are not usable as a basename
// prune, which is exactly why the hardcoded deny set has to exist.
func gitignoreNames(dir string) map[string]bool {
	out := map[string]bool{}
	if dir == "" {
		return out
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		clean := strings.TrimPrefix(strings.TrimSuffix(line, "/"), "/")
		if clean == "" || clean == "." || clean == ".." || strings.ContainsAny(clean, "/*?[") {
			continue
		}
		out[clean] = true
	}
	return out
}
