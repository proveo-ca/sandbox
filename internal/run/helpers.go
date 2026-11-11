// SPEC: _spec/internal/sbx/clone-workspace.puml
package run

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/envflag"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

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

// unknownModel is a ⚠ item when key, a model variable, names a model this build does not know.
func unknownModel(key, value, localModel string) []sandbox.Item {
	if localModel != "" || !strings.HasSuffix(key, "_MODEL") {
		return nil
	}
	if known, ok := provider.CheckModel(value); ok && !known {
		ui.Logf("%s=%q: a typo fails every call; a model newer than this binary works", key, value)
		return []sandbox.Item{{Warn: true, Text: fmt.Sprintf("%s=%q — not a model id this proveo build knows", key, value)}}
	}
	return nil
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
