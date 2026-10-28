// SPEC: _spec/internal/sbx/clone-workspace.puml
package run

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/envflag"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

func reportLinks(links []workspace.Link) {
	for _, l := range links {
		switch l.Action {
		case workspace.LinkMounted:
			ui.Storef("%s → %s (symlink leaves the workspace; target mounted)", l.Rel, l.Target)
		case workspace.LinkRefused:
			target := l.Target
			if target == "" {
				target = "(unresolved)"
			}
			ui.Warnf("%s → %s is not available inside the sandbox: %s", l.Rel, target, l.Reason)
		default:
			ui.Logf("%s: %s", l.Rel, l.Reason)
		}
	}
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

var warnedRetired sync.Map

// renamedEnv reads name, else the retired old name with a one-time deprecation warning.
func renamedEnv(name, old string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	v := os.Getenv(old)
	if v != "" {
		if _, seen := warnedRetired.LoadOrStore(old, true); !seen {
			ui.Warnf("%s is deprecated and goes away next release — set %s", old, name)
		}
	}
	return v
}

func StateDir() string {
	if x := renamedEnv("PROVEO_STATE_ROOT", "PROVEO_EGRESS_ROOT"); x != "" {
		return x
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "proveo")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "state", "proveo")
}

// RunRoots are the per-run state roots, current first, then the retired egress/ root.
func RunRoots() []string {
	state := StateDir()
	return []string{filepath.Join(state, "runs"), filepath.Join(state, "egress")}
}

// RunDirOf is the state directory of the run sid.
func RunDirOf(sid string) string { return filepath.Join(RunRoots()[0], sid) }
