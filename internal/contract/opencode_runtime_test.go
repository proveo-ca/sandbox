// SPEC: _spec/packages/lib/git-sync-turn.puml, _spec/packages/lib/seed-and-launch.puml
package contract_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeNativeGitSyncPluginLifecycle(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--experimental-vm-modules", "--test",
		filepath.Join(repoRoot(t), "internal", "contract", "testdata", "opencode_git_sync_plugin.mjs"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native plugin lifecycle: %v\n%s", err, out)
	} else {
		t.Logf("%s", out)
	}
}

func TestOpenCodeRuntimeEvidenceArguments(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	body := readRepoFile(t, "defs/opencode/entrypoint.sh")
	start := strings.Index(body, "\nOPENCODE_EVIDENCE_ARGS=()")
	if start < 0 {
		t.Fatal("runtime evidence section missing")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte("#!/usr/bin/env node\nconsole.log(JSON.stringify(process.argv.slice(2)))\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `source "$1/packages/lib/entrypoint-lib.sh"
PATH="$2:$PATH"
shift 2
` + body[start:]
	tests := []struct {
		name, evidence string
		args, want     []string
	}{
		{name: "root TUI", want: []string{"--log-level", "debug"}},
		{name: "continue TUI", args: []string{"--continue"}, want: []string{"--log-level", "debug", "--continue"}},
		{name: "session TUI", args: []string{"--session", "ses_123"}, want: []string{"--log-level", "debug", "--session", "ses_123"}},
		{name: "directory TUI", args: []string{"/workspace with spaces"}, want: []string{"--log-level", "debug", "/workspace with spaces"}},
		{name: "list", args: []string{"session", "list"}, want: []string{"--log-level", "debug", "session", "list"}},
		{name: "headless", args: []string{"run", "--standalone", "hello world"}, want: []string{"--log-level", "debug", "--print-logs", "run", "--thinking", "--standalone", "hello world"}},
		{name: "default headless", evidence: "default", args: []string{"run", "hello"}, want: []string{"run", "hello"}},
		{name: "launcher command", args: []string{"opencode", "--continue"}, want: []string{"--continue"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bashOrSkip(t), append([]string{"-c", script, "bash", repoRoot(t), bin}, tc.args...)...)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "PROVEO_AGENT_EVIDENCE=" + tc.evidence}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("runtime launch: %v\n%s", err, out)
			}
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			var got []string
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &got); err != nil {
				t.Fatalf("decode launched argv: %v\n%s", err, out)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("argv = %q, want %q", got, tc.want)
			}
		})
	}
}
