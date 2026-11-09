// SPEC: _spec/internal/choiceui/wireframe.puml
package run

import (
	"testing"

	"github.com/proveo-ca/proveo/internal/agentsettings"
)

func TestMiseTrustDefaultsOffAndOrdersOnBeforeOff(t *testing.T) {
	r := repoTrustRow("", false)
	if r.Label != "trust repo" || r.Options[r.Selected] != mcpOff || r.Locked || r.Options[0] != mcpOn || r.Options[1] != mcpOff || r.Cols[0] >= r.Cols[1] {
		t.Errorf("mise trust row = %+v", r)
	}
	if got := repoTrustRow(mcpOn, true); got.Options[got.Selected] != mcpOn || !got.Locked || got.Reason == "" {
		t.Errorf("env-pinned mise trust row = %+v", got)
	}
}

func TestMiseTrustCacheCannotOverrideExplicitOff(t *testing.T) {
	p := Params{MiseTrust: mcpOff, MiseTrustSet: true}
	p.seedFromCache(agentsettings.Choice{MiseTrust: mcpOn}, func(string) string { return "" }, false)
	if p.miseTrustAllowed() {
		t.Fatal("cached on overrode explicit off")
	}
	q := Params{}
	q.seedFromCache(agentsettings.Choice{MiseTrust: mcpOn}, func(string) string { return "" }, false)
	if !q.miseTrustAllowed() {
		t.Fatal("cached on was dropped")
	}
}
