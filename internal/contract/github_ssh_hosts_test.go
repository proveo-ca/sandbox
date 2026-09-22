// SPEC: _spec/packages/lib/github-ssh-hosts.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const githubMetaFixture = `{
  "ssh_keys": [
    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl",
    "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg="
  ]
}`

const githubEd25519 = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"

func TestHarnessEntrypointsEnsureGitHubGitTransport(t *testing.T) {
	t.Parallel()
	for _, rel := range agentEntrypoints {
		body := readRepoFile(t, rel)
		if !strings.Contains(body, "ensure_github_git_transport") {
			t.Errorf("%s must call ensure_github_git_transport so images without baked host keys still seed ~/.ssh/known_hosts", rel)
		}
	}
	seed := readRepoFile(t, "packages/lib/entrypoint-lib.sh")
	if !strings.Contains(seed, "seed_github_known_hosts \"$home\"") {
		t.Error("proveo_seed must seed GitHub known_hosts when /etc/ssh/ssh_known_hosts has none")
	}
	if strings.Contains(seed, "insteadOf") {
		t.Error("entrypoint-lib.sh must not rewrite git@github.com to HTTPS; SSH host keys are the fix")
	}
}

func TestBaseImageBakesGitHubSSHKnownHosts(t *testing.T) {
	t.Parallel()
	keys := readRepoFile(t, "defs/base/ssh/github_known_hosts")
	if !strings.Contains(keys, githubEd25519) {
		t.Error("defs/base/ssh/github_known_hosts must carry GitHub's published ed25519 host key")
	}
	df := readRepoFile(t, "defs/base/Dockerfile")
	if !strings.Contains(df, "COPY defs/base/ssh/github_known_hosts /etc/ssh/ssh_known_hosts") {
		t.Error("defs/base/Dockerfile must install GitHub host keys at /etc/ssh/ssh_known_hosts so SSH git works at t=0")
	}
}

func TestSeedGitHubKnownHostsWritesMetaKeysAndIsIdempotent(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	home := t.TempDir()
	meta := filepath.Join(t.TempDir(), "meta.json")
	if err := os.WriteFile(meta, []byte(githubMetaFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `set -euo pipefail
source "$1/packages/lib/entrypoint-lib.sh"
export HOME="$2" PROVEO_GITHUB_META_URL="file://$3" PROVEO_SSH_KNOWN_HOSTS="$2/no-system-hosts"
seed_github_known_hosts "$2"
seed_github_known_hosts "$2"`
	out, err := exec.Command(bash, "-c", script, "bash", repoRoot(t), home, meta).CombinedOutput()
	if err != nil {
		t.Fatalf("seed_github_known_hosts: %v\n%s", err, out)
	}
	body, err := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts"))
	if err != nil {
		t.Fatalf("known_hosts: %v\n%s", err, out)
	}
	if !strings.Contains(string(body), githubEd25519) {
		t.Errorf("known_hosts missing the meta ed25519 key:\n%s", body)
	}
	if n := strings.Count(string(body), "github.com "); n != 2 {
		t.Errorf("github.com entries = %d, want 2 after a second seed", n)
	}
}

func TestEnsureGitHubGitTransportDoesNotRewriteSSH(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	home := t.TempDir()
	script := `set -euo pipefail
source "$1/packages/lib/entrypoint-lib.sh"
export HOME="$2" PROVEO_GITHUB_META_URL="file:///no/such/github-meta.json"
unset GIT_CONFIG_COUNT || true
ensure_github_git_transport "$2"
printf 'COUNT=%s\n' "${GIT_CONFIG_COUNT:-}"
git config --get-all url.https://github.com/.insteadOf || true`
	out, err := exec.Command(bash, "-c", script, "bash", repoRoot(t), home).CombinedOutput()
	if err != nil {
		t.Fatalf("ensure_github_git_transport must return 0 when meta is unreachable: %v\n%s", err, out)
	}
	got := string(out)
	if strings.Contains(got, "git@github.com:") || strings.Contains(got, "ssh://git@github.com/") {
		t.Errorf("must not rewrite GitHub SSH remotes to HTTPS:\n%s", got)
	}
}
