// SPEC: _spec/internal/egress/egress-tiers.puml
package contract_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/chromebridge"
	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/entrypoint"
)

var legacyTiers = []string{"broker", "firewall", "proxy"}

func TestContainerBoundarySpeaksOnlyCanonicalTiers(t *testing.T) {
	const probeVar = "PROVEO_TEST_BROKERED_KEY"

	modes := egress.Modes()
	if strings.Join(modes, ",") != strings.Join(entrypoint.CanonicalTiers, ",") {
		t.Fatalf("the tier vocabulary has split:\n  egress.Modes()            = %v\n  entrypoint.CanonicalTiers = %v",
			modes, entrypoint.CanonicalTiers)
	}

	for _, tier := range modes {
		if !entrypoint.ShouldSkipEnvLoad(tier) {
			t.Errorf("%s: the prelude must keep the project .env out of the agent", tier)
		}
		t.Setenv(probeVar, "sk-real")
		if got := entrypoint.ApplyBrokerSentinel(tier, probeVar, ""); len(got) != 1 {
			t.Errorf("%s: a brokered key must be rewritten, got %v", tier, got)
		}
		if v := os.Getenv(probeVar); v != entrypoint.DefaultSentinel {
			t.Errorf("%s: brokered key is %q, want the sentinel %q", tier, v, entrypoint.DefaultSentinel)
		}
	}

	for _, legacy := range legacyTiers {
		if canonical, aliased := egress.Canonical(legacy); !aliased || canonical == legacy {
			t.Errorf("%q must still canonicalise for the CLI, got %q (aliased=%v)", legacy, canonical, aliased)
		}
		if entrypoint.ShouldSkipEnvLoad(legacy) {
			t.Errorf("%q reached the .env gate unrenamed and was understood", legacy)
		}
		t.Setenv(probeVar, "sk-real")
		if entrypoint.ApplyBrokerSentinel(legacy, probeVar, "") != nil {
			t.Errorf("%q reached the sentinel gate unrenamed and was understood", legacy)
		}
		if chromebridge.TierSupported(legacy, "forward") {
			t.Errorf("%q reached the host-bridge gate unrenamed and was understood", legacy)
		}
	}

	// The host-bridge gate is the last reader of "which tier leaves the agent a
	// route to the host". It used to be the daemon gate too — dind.ModeSupported,
	// asking the same question for a privileged sidecar — and the predicate
	// outlived the sidecar (_spec/_plans/retire-dind.puml). Same vocabulary, same
	// one tier, so the same assertion still belongs here.
	for _, tier := range modes {
		if want := tier == "open"; chromebridge.TierSupported(tier, "forward") != want {
			t.Errorf("chromebridge.TierSupported(%q, forward) = %v, want %v", tier, !want, want)
		}
		if chromebridge.TierSupported(tier, "broker") {
			t.Errorf("%q with brokered credentials must not pass the host-bridge gate", tier)
		}
	}
}

func TestBashPreludeNamesTheSameTiers(t *testing.T) {
	t.Parallel()
	path := filepath.Join(repoRoot(t), "packages", "lib", "entrypoint-lib.sh")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(b)

	want := strings.Join(entrypoint.CanonicalTiers, "|") + ")"
	if got := strings.Count(src, want); got != 2 {
		t.Errorf("entrypoint-lib.sh has %d %q case(s), want 2 (load_env + apply_broker_sentinel)", got, want)
	}
	for _, dead := range []string{"proxy|firewall)", "firewall|proxy)", "\n firewall) ", "\n broker) "} {
		if strings.Contains(src, dead) {
			t.Errorf("entrypoint-lib.sh still branches on %q — a name proveo run no longer emits", strings.TrimSpace(dead))
		}
	}
}
