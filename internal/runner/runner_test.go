// SPEC: _spec/tests/10-unit.puml

package runner

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestDockerRunArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{
			name: "minimal serve",
			cfg:  Config{Image: "proveo/opencode:latest", PidsLimit: 512},
			want: []string{"run", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--pids-limit=512", "proveo/opencode:latest"},
		},
		{
			name: "interactive with mounts, env, command",
			cfg: Config{
				Interactive: true, Remove: true, Name: "run1", User: "1000:1000",
				Tmpfs:     []string{"/tmp:noexec,nosuid,size=100m"},
				Mounts:    []Mount{{Host: "/repo", Container: "/app"}, {Host: "/repo/reports", Container: "/app/output"}},
				Env:       []string{"CLAUDE_CODE_OAUTH_TOKEN=t"},
				Image:     "proveo/claudecode:latest",
				Command:   []string{"--help"},
				PidsLimit: 512,
			},
			want: []string{
				"run", "-it", "--rm", "--name", "run1", "--user", "1000:1000",
				"--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--pids-limit=512",
				"--tmpfs", "/tmp:noexec,nosuid,size=100m",
				"-v", "/repo:/app", "-v", "/repo/reports:/app/output",
				"-e", "CLAUDE_CODE_OAUTH_TOKEN=t",
				"proveo/claudecode:latest", "--help",
			},
		},
		{
			name: "extra args (egress) land before the image",
			cfg: Config{
				ExtraArgs: []string{"--network", "sess-net", "-e", "HTTP_PROXY=http://mitm:8888"},
				Image:     "proveo/cursor:latest",
				PidsLimit: 1024,
			},
			want: []string{
				"run", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--pids-limit=1024",
				"--network", "sess-net", "-e", "HTTP_PROXY=http://mitm:8888",
				"proveo/cursor:latest",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DockerRunArgs(tc.cfg)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("DockerRunArgs(%+v) mismatch (-want +got):\n%s", tc.cfg, diff)
			}
		})
	}
}

func TestDockerRunArgsAlwaysHardened(t *testing.T) {
	t.Parallel()
	// The security baseline must appear on every run, regardless of config.
	got := strings.Join(DockerRunArgs(Config{Image: "x", PidsLimit: 512}), " ")
	for _, flag := range Hardening(512) {
		if !strings.Contains(got, flag) {
			t.Errorf("DockerRunArgs() = %q, missing mandatory hardening flag %q", got, flag)
		}
	}
}

func TestDockerRunArgsAutoPidsWhenUnset(t *testing.T) {
	t.Parallel()
	got := strings.Join(DockerRunArgs(Config{Image: "proveo/opencode:latest"}), " ")
	if !strings.Contains(got, "--pids-limit=") {
		t.Errorf("auto-resolve must still apply --pids-limit: %s", got)
	}
	if !strings.Contains(got, "--cap-drop=ALL") {
		t.Errorf("missing cap-drop: %s", got)
	}
}
