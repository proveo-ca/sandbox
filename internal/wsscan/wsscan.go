// Package wsscan finds marker files under a workspace scope.
// SPEC: _spec/internal/wsscan/workspace-scan.puml, _spec/internal/agentsettings/choice-cache.puml
package wsscan

import (
	"os"
	"path/filepath"
	"strings"
)

const MaxDepth = 7

const DefaultBudget = 20000

var alwaysPrune = map[string]bool{
	"node_modules": true, ".git": true, "vendor": true, "target": true,
	"dist": true, "build": true, ".venv": true, "venv": true,
	"__pycache__": true, ".next": true, ".turbo": true, ".nx": true,
	".cache": true, ".gradle": true, ".tox": true, ".mypy_cache": true,
	".pytest_cache": true, ".ruff_cache": true, "Pods": true, ".terraform": true,
	".pnpm-store": true, ".npm": true, ".yarn": true,
}

func AlwaysPrune(name string) bool { return alwaysPrune[name] }

type Marker struct {
	Label    string
	Names    []string
	Suffixes []string
}

type Result struct {
	Found     map[string]bool
	Truncated bool
}

func (r Result) Has(label string) bool { return r.Found[label] }

func (r Result) Labels(markers []Marker) []string {
	var out []string
	for _, m := range markers {
		if r.Found[m.Label] {
			out = append(out, m.Label)
		}
	}
	return out
}

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
