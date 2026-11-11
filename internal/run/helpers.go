// SPEC: _spec/internal/sbx/clone-workspace.puml, _spec/internal/workspace/mount-symlink-escape.puml
package run

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/envflag"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

func reportLinks(links []workspace.Link) {
	for _, ln := range linkLines(links) {
		switch ln.to {
		case lineScreen:
			ui.Storef("%s", ln.text)
		case lineWarn:
			ui.Warnf("%s", ln.text)
		default:
			ui.Logf("%s", ln.text)
		}
	}
}

// linkLine is one line reportLinks prints, and where it goes.
type linkLine struct {
	text string
	to   lineTo
}

type lineTo int

const (
	lineScreen lineTo = iota
	lineWarn
	lineLog // the run log only
)

// linkLines words each link decision in walk order. Mounted links that share a
// directory and a target directory, each keeping its target's name, take one
// screen line together; each of them still gets its own line in the run log.
func linkLines(links []workspace.Link) []linkLine {
	groups := map[[2]string]int{}
	for _, l := range links {
		if k, ok := linkDirs(l); ok {
			groups[k]++
		}
	}
	said := map[[2]string]bool{}
	var out []linkLine
	for _, l := range links {
		switch l.Action {
		case workspace.LinkMounted:
			one := l.Rel + " → " + l.Target + " (symlink leaves the workspace; target mounted)"
			k, ok := linkDirs(l)
			if !ok || groups[k] < 2 {
				out = append(out, linkLine{one, lineScreen})
				continue
			}
			if !said[k] {
				said[k] = true
				out = append(out, linkLine{k[0] + "/ → " + k[1] + "/ (" + strconv.Itoa(groups[k]) +
					" symlinks leave the workspace; targets mounted)", lineScreen})
			}
			out = append(out, linkLine{one, lineLog})
		case workspace.LinkRefused:
			target := l.Target
			if target == "" {
				target = "(unresolved)"
			}
			out = append(out, linkLine{l.Rel + " → " + target + " is not available inside the sandbox: " + l.Reason, lineWarn})
		default:
			out = append(out, linkLine{l.Rel + ": " + l.Reason, lineLog})
		}
	}
	return out
}

// linkDirs is a mounted link's directory and its target's, when the link sits below the workspace root and keeps its target's name.
func linkDirs(l workspace.Link) ([2]string, bool) {
	dir := filepath.Dir(l.Rel)
	if l.Action != workspace.LinkMounted || dir == "." || filepath.Base(l.Rel) != filepath.Base(l.Target) {
		return [2]string{}, false
	}
	return [2]string{dir, filepath.Dir(l.Target)}, true
}

func CloneDefault(getenv func(string) string) bool { return envflag.Bool(getenv("PROVEO_CLONE"), true) }

func mountRootDeps(getenv func(string) string) bool {
	return envflag.Bool(getenv("PROVEO_MOUNT_ROOT_DEPS"), true)
}

// sbxStoredAuth names the subscription credentials sbx's secret store holds for man.
func sbxStoredAuth(man manifest.Manifest) []string {
	if !man.Subscription {
		return nil
	}
	return credentials.StoreHolds(man, sbx.StoredServices())
}

func gitRootOrEmpty(ws workspace.Scope, repoRoot string) string {
	if !ws.IsRepo {
		return ""
	}
	return repoRoot
}

func warnUnknownModel(key, value, localModel string) {
	if localModel != "" || !strings.HasSuffix(key, "_MODEL") {
		return
	}
	if known, ok := provider.CheckModel(value); ok && !known {
		ui.Warnf("%s=%q is not a model id this proveo build recognizes — if it is a typo the agent will "+
			"fail on every call; if it is newer than this binary, ignore this.", key, value)
	}
}

func OrWD(p string) string {
	if p != "" {
		return p
	}
	wd, _ := os.Getwd()
	return wd
}

func StateDir() string {
	if x := os.Getenv("PROVEO_STATE_ROOT"); x != "" {
		return x
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "proveo")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "state", "proveo")
}

// RunRoots are the per-run state roots.
func RunRoots() []string {
	state := StateDir()
	return []string{filepath.Join(state, "runs")}
}

// RunDirOf is the state directory of the run sid.
func RunDirOf(sid string) string { return filepath.Join(RunRoots()[0], sid) }
