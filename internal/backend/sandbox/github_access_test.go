// SPEC: _spec/cmd/proveo/github-access.puml
package sandbox

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/githubaccess"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestSSHGrantAndSocketReachTheSeedOnlyWhenChosen(t *testing.T) {
	for _, mode := range []string{githubaccess.SSH, githubaccess.HTTPS, githubaccess.Later, ""} {
		in := Input{Target: "codex", GitTransport: mode, Lookup: func(string) string { return "" }}
		cfg, w, _ := Spec(in)
		granted := slices.ContainsFunc(w.Descriptor.Capabilities, func(c sbx.CapabilityV3) bool { return c.Type == sbx.CapSSHAgent })
		if granted != (mode == githubaccess.SSH) {
			t.Fatalf("%s SSH grant=%v", mode, granted)
		}
		if slices.Contains(hookEnv(in, cfg.Env), "SSH_AUTH_SOCK") != (mode == githubaccess.SSH) {
			t.Fatalf("%s seed socket visibility incorrect", mode)
		}
		if slices.Contains(cfg.Env, "GIT_CONFIG_COUNT=2") != (mode == githubaccess.HTTPS) {
			t.Fatalf("%s HTTPS rewriting incorrect", mode)
		}
	}
}

func TestHTTPSRewritesCoexistWithRepositorySafetyInTheActualGitEnvironment(t *testing.T) {
	t.Parallel()
	root := realPath(t.TempDir())
	for _, mode := range []string{githubaccess.HTTPS, githubaccess.SSH, githubaccess.Later} {
		cfg, _, _ := Spec(Input{Target: "codex", RepoRoot: root, GitTransport: mode, Lookup: func(string) string { return "" }})
		for _, source := range []string{"git@github.com:owner/repo.git", "ssh://git@github.com/owner/repo.git"} {
			cmd := exec.Command("git", "ls-remote", "--get-url", source)
			cmd.Env = append(os.Environ(), cfg.Env...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Git URL resolution: %v %s", err, out)
			}
			want := source
			if mode == githubaccess.HTTPS {
				want = "https://github.com/owner/repo.git"
			}
			if strings.TrimSpace(string(out)) != want {
				t.Fatalf("%s resolved %s to %s, want %s", mode, source, out, want)
			}
		}
		cmd := exec.Command("git", "config", "--get-all", "safe.directory")
		cmd.Env = append(os.Environ(), cfg.Env...)
		out, err := cmd.CombinedOutput()
		if err != nil || !slices.Contains(strings.Split(strings.TrimSpace(string(out)), "\n"), root) {
			t.Fatalf("%s lost repository safety: %v %s", mode, err, out)
		}
	}
}
