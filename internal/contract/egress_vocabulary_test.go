// SPEC: _spec/_plans/revision-env-egress.puml, _spec/internal/egress/egress-tiers.puml
package contract_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/dind"
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
		if dind.ModeSupported(legacy) {
			t.Errorf("%q reached the daemon gate unrenamed and was understood", legacy)
		}
	}

	for _, tier := range modes {
		if want := tier == "open"; dind.ModeSupported(tier) != want {
			t.Errorf("dind.ModeSupported(%q) = %v, want %v", tier, !want, want)
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
