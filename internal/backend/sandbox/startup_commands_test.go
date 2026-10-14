// SPEC: _spec/internal/sbx/startup-dispatch.puml
package sandbox

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/workspace"
)

func TestStartupCommandsRunInstallsLastAndInTheBackground(t *testing.T) {
	t.Parallel()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	worktree := Input{Target: "claudecode", Clone: true, RepoRoot: filepath.Join(root, "wt"),
		CloneSource: workspace.WorktreeSource{Main: filepath.Join(root, "main"), Ref: "feat"}}
	plain := Input{Target: "claudecode"}
	release, provision, entry := sbx.SeedCommand("claudecode"), sbx.SeedProvisionCommand("claudecode"), sbx.SeedEntrypointCommand("claudecode")
	full := sbx.SeedFullCommand("cecli")
	for _, tc := range []struct {
		name     string
		in       Input
		ownAgent bool
		want     []string
	}{
		{"built-in agent mixin", plain, false, []string{release.Description, entry.Description, provision.Description}},
		{"worktree mixin", worktree, false, []string{"", release.Description, entry.Description, provision.Description}},
		{"own agent seeds inline", Input{Target: "cecli"}, true, []string{full.Description}},
		{"shell-launched def seeds inline", Input{Target: "cecli"}, false, []string{full.Description, entry.Description}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmds := startupCommands(tc.in, tc.ownAgent)
			var got []string
			for _, c := range cmds {
				if c.User == "root" {
					got = append(got, "")
					continue
				}
				got = append(got, c.Description)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("startupCommands(%s) order = %q, want %q", tc.name, got, tc.want)
			}
			split := sbx.BuiltinAgent(tc.in.Target) != ""
			for i, c := range cmds {
				if last := split && i == len(cmds)-1; c.Background != last {
					t.Errorf("startupCommands(%s)[%d] %q Background = %v, want %v — only the trailing install step may run beside the agent",
						tc.name, i, c.Description, c.Background, last)
				}
			}
		})
	}
}
