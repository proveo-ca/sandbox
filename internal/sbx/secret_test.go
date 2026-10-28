package sbx

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// SPEC: _spec/internal/sbx/credential-path.puml (THE CUSTOM ROUTE, N14)
func TestCustomSecretsCarryTheirOwnHostsAndVariable(t *testing.T) {
	t.Parallel()

	args := SecretSetCustomArgs([]string{"opencode.ai", "*.opencode.ai"}, "OPENCODE_API_KEY")
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"secret set-custom", "--host opencode.ai", "--host *.opencode.ai",
		"--env OPENCODE_API_KEY",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("SecretSetCustomArgs = %q, missing %q", joined, want)
		}
	}
	// The value never becomes an argument: `--value` and `--token` are both
	// documented as "visible in shell history", argv is visible to ps, and the
	// suite asserts no provider key appears there. It goes on stdin.
	for _, banned := range []string{"--value", "--token", "-t"} {
		for _, a := range args {
			if a == banned {
				t.Errorf("SecretSetCustomArgs passes %s — the value must go on stdin", banned)
			}
		}
	}
}

func TestBuiltinServicesDecideWhichCommandStores(t *testing.T) {
	t.Parallel()

	for _, known := range []string{"anthropic", "openai", "xai", "google", "cursor", "github"} {
		if !IsBuiltinService(known) {
			t.Errorf("%q is in `sbx secret set --help`, so a plain service secret reaches it", known)
		}
	}
	// Every one of these is a provider proveo's registry knows and sbx does not.
	for _, unknown := range []string{"opencode", "perplexity", "venice", "zai", "moonshot", "cerebras"} {
		if IsBuiltinService(unknown) {
			t.Errorf("%q is not in sbx's service list; storing it plainly gives sbx a value "+
				"with nowhere to put it", unknown)
		}
	}
}

// SPEC: _spec/internal/sbx/backed-api-key-env.puml
func TestAPIKeyServicesAreTheStoredRowsOnly(t *testing.T) {
	restore := sh
	t.Cleanup(func() { sh = restore })
	sh.SecretList = func() ([]byte, error) {
		return []byte(`SCOPE      TYPE      NAME                      SECRET
(global)   service   CURSOR_API_KEY            (stored)
(global)   service   cursor                    (stored)
(global)   service   github                    (stored)
(global)   service   anthropic                 (oauth configured)
(global)   service   openai                    (oauth configured)

CUSTOM SECRETS
SCOPE      TARGETS                       ENV                    PLACEHOLDER               SECRET
(global)   api.example.com               EXAMPLE_KEY            sbx-cs-abc                (stored)
`), nil
	}
	got := APIKeyServices()
	want := []string{"CURSOR_API_KEY", "cursor", "github"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("APIKeyServices() mismatch (-want +got):\n%s", diff)
	}
}
