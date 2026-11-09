// SPEC: _spec/cmd/proveo/github-access.puml
package contract_test

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestGitHubSSHVerificationRequiresAnAgentAndAcceptsGitHubExitOne(t *testing.T) {
	for _, tc := range []struct {
		name, mode, greeting, repoURL      string
		socket                             bool
		agentStatus, sshStatus, repoStatus int
		want                               bool
	}{
		{name: "HTTPS needs no agent", mode: "https", want: true},
		{name: "missing socket", mode: "ssh", sshStatus: 1},
		{name: "empty agent", mode: "ssh", socket: true, agentStatus: 1, sshStatus: 1},
		{name: "unregistered key", mode: "ssh", socket: true, greeting: "Permission denied (publickey).", sshStatus: 255},
		{name: "wrong greeting", mode: "ssh", socket: true, greeting: "some error", sshStatus: 1},
		{name: "GitHub success exits one", mode: "ssh", socket: true, greeting: "Hi executor! You've successfully authenticated, but GitHub does not provide shell access.", sshStatus: 1, want: true},
		{name: "repository read denied", mode: "ssh", socket: true, greeting: "Hi executor! You've successfully authenticated, but GitHub does not provide shell access.", sshStatus: 1, repoURL: "git@github.com:owner/private.git", repoStatus: 128},
		{name: "repository read verified", mode: "ssh", socket: true, greeting: "Hi executor! You've successfully authenticated, but GitHub does not provide shell access.", sshStatus: 1, repoURL: "ssh://git@github.com/owner/repo.git", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sock := filepath.Join(dir, "agent.sock")
			if tc.socket {
				l, err := net.Listen("unix", sock)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = l.Close() })
			}
			refused := filepath.Join(dir, "refused")
			script := `source "$1/packages/lib/entrypoint-lib.sh"
_proveo_bounded() { shift; "$@"; }
ssh-add() { return "$TEST_AGENT_STATUS"; }
ssh() { printf '%s\n' "$TEST_GREETING"; return "$TEST_SSH_STATUS"; }
git() {
 case "$3" in
  remote) printf '%s\n' "$TEST_REPO_URL" ;;
  ls-remote) return "$TEST_REPO_STATUS" ;;
  *) return 1 ;;
 esac
}
_proveo_scan_root() { printf '%s\n' "$TEST_WORK"; }
proveo_verify_github_ssh`
			cmd := exec.Command(bashOrSkip(t), "-c", script, "bash", repoRoot(t))
			cmd.Env = append(os.Environ(), "BASH_ENV=", "PROVEO_GITHUB_TRANSPORT="+tc.mode, "SSH_AUTH_SOCK="+sock, "PROVEO_SEED_REFUSED="+refused,
				"TEST_GREETING="+tc.greeting, "TEST_WORK="+dir,
				"TEST_AGENT_STATUS="+strconv.Itoa(tc.agentStatus), "TEST_SSH_STATUS="+strconv.Itoa(tc.sshStatus),
				"TEST_REPO_URL="+tc.repoURL, "TEST_REPO_STATUS="+strconv.Itoa(tc.repoStatus))
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.want {
				t.Fatalf("result %v, want success %v:\n%s", err, tc.want, out)
			}
			if tc.want {
				if tc.repoURL != "" && !strings.Contains(string(out), "push permission remains unverified") {
					t.Fatalf("repository check omitted its write-access limitation: %s", out)
				}
				if _, err := os.Stat(refused); err == nil {
					t.Fatal("successful check left refusal")
				}
				return
			}
			if b, err := os.ReadFile(refused); err != nil || !strings.Contains(string(b), "proveo init") {
				t.Fatalf("no repair diagnostic: %s %v", b, err)
			}
			launch := exec.Command("sh", filepath.Join(repoRoot(t), "packages/lib/proveo-await-seed"), "sh", "-c", "echo AGENT_LAUNCHED")
			launch.Env = append(os.Environ(), "SANDBOX_NAME=test", "PROVEO_WORKDIR="+dir, "PROVEO_SEED_REFUSED="+refused, "PROVEO_INSTRUCTIONS_MARKER="+filepath.Join(dir, "marker"), "PROVEO_INSTRUCTIONS_WAIT=0")
			out, err = launch.CombinedOutput()
			if err == nil || strings.Contains(string(out), "AGENT_LAUNCHED") {
				t.Fatalf("failed SSH check released agent: %v %s", err, out)
			}
		})
	}
}

func TestSSHModeDoesNotLaunchBeforeSeedRelease(t *testing.T) {
	for _, tc := range []struct {
		name, mode          string
		seeded, debug, want bool
	}{
		{"pending SSH refuses launch", "ssh", false, false, false},
		{"released SSH permits launch", "ssh", true, false, true},
		{"HTTPS retains timeout behavior", "https", false, false, true},
		{"repair can inspect the directory", "ssh", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "seeded")
			if tc.seeded {
				if err := os.WriteFile(marker, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{filepath.Join(repoRoot(t), "packages/lib/proveo-await-seed"), "sh", "-c", "echo AGENT_LAUNCHED"}
			if tc.debug {
				args = []string{args[0], "--cwd"}
			}
			cmd := exec.Command("sh", args...)
			cmd.Env = append(os.Environ(), "SANDBOX_NAME=test", "PROVEO_WORKDIR="+dir, "PROVEO_INSTRUCTIONS_WAIT=0",
				"PROVEO_GITHUB_TRANSPORT="+tc.mode, "PROVEO_INSTRUCTIONS_MARKER="+marker, "PROVEO_SEED_REFUSED="+filepath.Join(dir, "refused"))
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.want || (!tc.want && strings.Contains(string(out), "AGENT_LAUNCHED")) {
				t.Fatalf("launch result %v, want %v: %s", err, tc.want, out)
			}
			if tc.mode == "ssh" && strings.Contains(string(out), "launching anyway") {
				t.Fatalf("SSH pending check claimed to launch anyway: %s", out)
			}
		})
	}
}
