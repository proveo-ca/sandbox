//go:build e2e

// SPEC: _spec/cmd/proveo/github-access.puml
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/githubaccess"
)

func TestInitGitHubSSHChoicePersistsWithoutChangingHostProtocol(t *testing.T) {
	r := startInitTools(t, 140, map[string]string{
		"gh": `#!/bin/sh
case "$1 $2" in
 "auth token") echo test-token ;;
 "config get") echo ssh ;;
 "api --hostname") echo executor ;;
 *) echo "unexpected gh mutation" >&2; exit 1 ;;
esac
`,
		"ssh-add": "#!/bin/sh\necho '256 SHA256:test-key test (ED25519)'\n",
	}, "--skip-login", "--skip-setup", "--skip-git")
	r.page(t, 1)
	r.keys(t, "Enter")
	screen := r.wait(t, "GitHub access — API login and sandbox Git")
	for _, text := range []string{"SSH agent", "HTTPS token", "SHA256:test-key", "verification pending"} {
		if !strings.Contains(screen, text) {
			t.Fatalf("missing %q:\n%s", text, screen)
		}
	}
	r.keys(t, "Enter")
	r.page(t, 2)
	var saved string
	_ = filepath.WalkDir(r.home, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "github.json" {
			saved = path
		}
		return nil
	})
	if saved == "" {
		t.Fatal("GitHub access selection was not saved")
	}
	cfg, err := githubaccess.Load(saved)
	if err != nil || cfg.Transport != githubaccess.SSH {
		t.Fatalf("saved transport %v %v", cfg, err)
	}
	for _, call := range r.calls(t) {
		if strings.HasPrefix(call, "settings set") || strings.HasPrefix(call, "daemon restart") {
			t.Fatalf("ready host unexpectedly mutated: %s", call)
		}
	}
	r.skip(t)
	r.page(t, 3)
	r.finish(t)
	r.waitExit(t)
}

func TestInitYesReportsSSHWithoutPromptingOrGrantingAccess(t *testing.T) {
	r := startInitTools(t, 140, map[string]string{
		"gh": `#!/bin/sh
case "$1 $2" in
 "auth token") exit 1 ;;
 "config get") echo ssh ;;
 "api --hostname") exit 1 ;;
 *) echo UNEXPECTED_GH_INTERACTION; exit 1 ;;
esac
`,
		"ssh-add": "#!/bin/sh\necho '256 SHA256:test-key test (ED25519)'\n",
	}, "--yes", "--skip-login", "--skip-setup", "--skip-git")
	all := r.waitExit(t)
	if !strings.Contains(all, "sandbox verification pending") || strings.Contains(all, "UNEXPECTED_GH_INTERACTION") || strings.Contains(all, "page ") {
		t.Fatalf("noninteractive SSH report:\n%s", all)
	}
	if err := filepath.WalkDir(r.home, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "github.json" {
			t.Errorf("--yes saved a new transport grant: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, call := range r.calls(t) {
		if strings.HasPrefix(call, "settings set") || strings.HasPrefix(call, "daemon restart") {
			t.Fatalf("--yes mutated SSH settings: %s", call)
		}
	}
}
