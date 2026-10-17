// SPEC: _spec/internal/sbx/clone-workspace.puml
package run

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/proveo-ca/proveo/internal/credentials"
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

func CloneDefault(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("PROVEO_CLONE"))) {
	case "0", "off", "no", "false", "disable", "disabled":
		return false
	}
	return true
}

func mountRootDeps(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("PROVEO_MOUNT_ROOT_DEPS"))) {
	case "0", "off", "no", "false", "disable", "disabled":
		return false
	}
	return true
}

// sbxStoredAuth names the subscription credentials sbx's secret store holds for man.
func sbxStoredAuth(man manifest.Manifest) []string {
	if !man.Subscription {
		return nil
	}
	return credentials.StoreHolds(man, sbx.StoredSecretNames())
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
	if x := os.Getenv("PROVEO_EGRESS_ROOT"); x != "" {
		return x
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "proveo")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "state", "proveo")
}

func WizardEnabled() bool {
	switch strings.ToLower(os.Getenv("PROVEO_WIZARD")) {
	case "off", "0", "no", "false", "disable", "disabled":
		return false
	}
	return true
}
