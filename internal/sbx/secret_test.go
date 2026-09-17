package sbx

import (
	"strings"
	"testing"
)

// sbx injects a plain service secret only for the services it knows. Measured
// 2026-09-15: with `opencode` stored under BOTH the env var name and the
// service name, and the value re-piped without the trailing newline the first
// attempt carried, --credentials broker still answered HTTP 401 while the same
// key forwarded to 200. sbx holds the value and knows neither the host to match
// nor the header to write. A custom secret carries both with it.
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
