// SPEC: _spec/cmd/proveo/github-access.puml
package githubaccess

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTransportPersistsAndRejectsUnknownModes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config", "github.json")
	if c, err := Load(path); err != nil || c.Transport != HTTPS {
		t.Fatalf("default: %v %v", c, err)
	}
	for _, mode := range []string{SSH, HTTPS, Later} {
		if err := Save(path, mode); err != nil {
			t.Fatal(err)
		}
		if c, err := Load(path); err != nil || c.Transport != mode {
			t.Fatalf("saved: %v %v", c, err)
		}
	}
	if err := Save(path, "unexpected"); err == nil {
		t.Fatal("accepted invalid mode")
	}
	if err := os.WriteFile(path, []byte(`{"transport":"unexpected"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("loaded invalid mode")
	}
}

func TestHostInspectionUsesTheConfiguredAgentAndOnlyReadsMetadata(t *testing.T) {
	t.Parallel()
	var calls []string
	command := func(env []string, name string, args ...string) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)
		switch call {
		case "sbx settings get ssh.agentForwardingEnabled":
			return []byte("true\n"), nil
		case "sbx settings get ssh.agentSocketPath":
			return []byte("/stable/agent.sock\n"), nil
		case "gh config get git_protocol --host github.com":
			return []byte("ssh\n"), nil
		case "gh api --hostname github.com user --jq .login":
			return []byte("executor\n"), nil
		case "ssh-add -l":
			if !slices.Contains(env, "SSH_AUTH_SOCK=/stable/agent.sock") {
				t.Fatalf("wrong agent environment: %v", env)
			}
			return []byte("256 SHA256:one host-key (ED25519)\n256 SHA256:two another (ED25519)\n"), nil
		}
		return nil, errors.New("unexpected command")
	}
	h := Inspect("sbx", command, func(string) string { return "/transient/socket" })
	if h.Protocol != SSH || h.Login != "executor" || h.SSHProblem() != "" || len(h.Keys) != 2 {
		t.Fatalf("host: %#v", h)
	}
	for _, call := range calls {
		if strings.Contains(call, "auth token") || strings.Contains(call, " set ") {
			t.Fatalf("inspection mutated settings or read a token: %s", call)
		}
	}
}

func TestUnknownForwardingIsNotReady(t *testing.T) {
	t.Parallel()
	h := InspectSSH("sbx", func([]string, string, ...string) ([]byte, error) { return nil, errors.New("unavailable") }, func(string) string { return "" })
	if h.SSHProblem() == "" || len(h.Keys) != 0 {
		t.Fatalf("unavailable agent reported ready: %#v", h)
	}
}
