// SPEC: _spec/cmd/proveo/github-access.puml
package main

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/githubaccess"
)

func TestGitHubPanelFollowsHostTransportWithoutClaimingSandboxVerification(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{githubaccess.SSH, githubaccess.HTTPS} {
		h := githubaccess.Host{Protocol: mode, Login: "executor", Keys: []string{"SHA256:one"}, Forwarding: true, ForwardingKnown: true}
		f := githubAccessForm(h, "")
		want := githubHTTPS
		if mode == githubaccess.SSH {
			want = githubSSH
		}
		if f.Selection("sandbox Git") != want {
			t.Fatalf("host %s chose %s", mode, f.Selection("sandbox Git"))
		}
		if !strings.Contains(strings.Join(f.Header, "\n"), "verification pending") {
			t.Fatal("host check claimed sandbox verification")
		}
		if githubAccessForm(h, githubaccess.Later).Selection("sandbox Git") != "later" {
			t.Fatal("ignored persisted deferral")
		}
	}
}
