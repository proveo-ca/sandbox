// SPEC: _spec/internal/devports/dev-ports.puml
package devports

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// AndroidApp is one Gradle application module and the package it installs.
type AndroidApp struct {
	Module string // Gradle path: ":app"; "" for an application at the build root
	AppID  string
	Source string // workspace-relative build file
}

// Label is the one-line form a choice row prints.
func (a AndroidApp) Label() string {
	name := a.Module
	if name == "" {
		name = ":"
	}
	return name + " " + a.AppID
}

// Task is the Gradle task that builds and installs the debug variant.
func (a AndroidApp) Task() string {
	if a.Module == "" {
		return "installDebug"
	}
	return a.Module + ":installDebug"
}

var (
	androidAppPlugin = regexp.MustCompile(`com\.android\.application|android\.application\b`)
	applicationID    = regexp.MustCompile(`\bapplicationId\s*=?\s*["']([\w.]+)["']`)
	namespaceID      = regexp.MustCompile(`\bnamespace\s*=?\s*["']([\w.]+)["']`)
)

// AndroidApps walks root for application modules, ordered by module path.
func AndroidApps(root string) []AndroidApp {
	var out []AndroidApp
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if path != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			if rel != "." && strings.Count(rel, string(filepath.Separator)) >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "build.gradle" && d.Name() != "build.gradle.kts" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil || !androidAppPlugin.Match(b) {
			return nil
		}
		id := firstGroup(applicationID, b)
		if id == "" {
			id = firstGroup(namespaceID, b)
		}
		if id == "" {
			return nil
		}
		module := ""
		if dir := filepath.Dir(rel); dir != "." {
			module = ":" + strings.ReplaceAll(filepath.ToSlash(dir), "/", ":")
		}
		out = append(out, AndroidApp{Module: module, AppID: id, Source: filepath.ToSlash(rel)})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out
}

func firstGroup(re *regexp.Regexp, b []byte) string {
	if m := re.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}
