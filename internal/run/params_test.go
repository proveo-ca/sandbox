package run

import (
	"testing"

	"github.com/proveo-ca/proveo/internal/agentsettings"
)

func TestCachedBaselineNeverBecomesAnEgressMode(t *testing.T) {
	for _, baseline := range []string{"allow-all", "balanced", "deny-all", "unreadable"} {
		var p Params
		p.seedFromCache(agentsettings.Choice{Egress: baseline}, func(string) string { return "" }, false)
		if p.Mode != "" {
			t.Errorf("a cached %q seeded p.Mode = %q; only an egress mode may", baseline, p.Mode)
		}
	}
	var p Params
	p.seedFromCache(agentsettings.Choice{Egress: "open"}, func(string) string { return "" }, false)
	if p.Mode != "open" {
		t.Errorf("a cached egress mode must still seed p.Mode, got %q", p.Mode)
	}
}
