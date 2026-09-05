package run

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gdamore/tcell/v2"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/ptyproxy"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

func policyProviderHosts(detected []string, c manifest.Capabilities) []string {
	seen, out := map[string]bool{}, []string{}
	for _, h := range append(credentials.ReachableHosts(detected), credentials.ReachableHosts(c.Providers)...) {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

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

// SPEC: _spec/internal/sbx/clone-workspace.puml
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

func reviewConsent(mode string) (func(host, port string) bool, *ptyproxy.Proxy) {
	if mode != "review" {
		return nil, nil
	}
	if !ptyproxy.Usable(os.Stdin, os.Stdout) {
		ui.Warnf("review tier without a terminal: no way to ask, so every new connection will be denied")
		return nil, nil
	}
	proxy := ptyproxy.New(os.Stdin, os.Stdout)
	return func(host, port string) bool {
		allowed := false
		if err := proxy.Overlay(func(in io.Reader, _ io.Writer) error {
			var derr error
			allowed, derr = choiceui.Consent(func() (tcell.Screen, error) {
				return proxy.OverlayScreen(in)
			}, host, port)
			return derr
		}); err != nil {
			ui.Warnf("review prompt unavailable (%v): denying %s:%s", err, host, port)
			return false
		}
		return allowed
	}, proxy
}

func sbxStoredAuth(man manifest.Manifest, p *Params) []string {
	if !sbxSuppliesCredential(man, p, true) {
		return nil
	}
	ok, _ := sbx.Available()
	if !sbxSuppliesCredential(man, p, ok) {
		return nil
	}
	return credentials.StoreHolds(man, sbx.StoredSecretNames())
}

func sbxSuppliesCredential(man manifest.Manifest, p *Params, sbxOK bool) bool {
	return man.Subscription && man.IsSbx() && p.Mode != "review" &&
		sandbox.Enabled() && sbxOK
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

func ollamaModelsDir() string {
	if d := os.Getenv("PROVEO_OLLAMA_MODELS_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".ollama", "models")
}

func preferHostOllama() bool {
	if os.Getenv("PROVEO_LOCAL_MODEL_SIDECAR") == "1" {
		return false
	}
	return runtime.GOOS == "darwin"
}

func sidecarOllamaGPU() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	out, err := exec.Command("docker", "info", "--format", "{{json .Runtimes}}").Output()
	return err == nil && strings.Contains(string(out), "nvidia")
}

func OrWD(p string) string {
	if p != "" {
		return p
	}
	wd, _ := os.Getwd()
	return wd
}

func brokerEnabled() bool {
	switch strings.ToLower(os.Getenv("PROVEO_CREDENTIAL_BROKER")) {
	case "off", "0", "no", "false", "disable", "disabled":
		return false
	}
	return true
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
