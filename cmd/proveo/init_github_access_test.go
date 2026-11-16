// SPEC: _spec/cmd/proveo/github-access.puml
package main

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/gitaccess"
)

// NO TTY: unasked, the access fact keeps SSH's state; a how-to only when no terminal could ask, and one.
func TestAnUnaskedAccessKeepsSSHsStateAndAHowToOnlyWithoutATerminal(t *testing.T) {
	t.Parallel()
	h := gitaccess.Host{ForwardingKnown: true, Forwarding: true,
		AgentError: "host agent unavailable or empty; load a GitHub authentication key with ssh-add"}
	for _, noTerminal := range []bool{false, true} {
		f := unaskedAccess(h, "", noTerminal)
		want := []string{"0 host keys; sandbox verification pending", "host agent unavailable or empty"}
		if noTerminal {
			want = append(want, "`proveo init` from a terminal signs in and chooses it")
		}
		if f.Value != "not chosen" || strings.Join(f.Detail, "\n") != strings.Join(want, "\n") {
			t.Errorf("noTerminal=%v: access = %q %q, want %q", noTerminal, f.Value, f.Detail, want)
		}
	}
}

func TestGitHubPanelFollowsHostTransportWithoutClaimingSandboxVerification(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{gitaccess.SSH, gitaccess.HTTPS} {
		h := gitaccess.Host{Protocol: mode, Login: "executor", Keys: []string{"SHA256:one"}, Forwarding: true, ForwardingKnown: true}
		f := githubAccessForm(h, "")
		want := githubHTTPS
		if mode == gitaccess.SSH {
			want = githubSSH
		}
		if f.Selection("sandbox Git") != want {
			t.Fatalf("host %s chose %s", mode, f.Selection("sandbox Git"))
		}
		if !strings.Contains(strings.Join(f.Header, "\n"), "verification pending") {
			t.Fatal("host check claimed sandbox verification")
		}
		if githubAccessForm(h, gitaccess.Later).Selection("sandbox Git") != "later" {
			t.Fatal("ignored persisted deferral")
		}
	}
}
