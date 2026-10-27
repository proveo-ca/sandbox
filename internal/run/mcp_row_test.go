// SPEC: _spec/internal/sbx/sbx-kit-v3.puml
package run

import (
	"testing"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/choiceui"
)

func TestTheMCPRowAppearsOnlyForAKitWithGatewayHooks(t *testing.T) {
	if r := applicableRows(mcpRow("claudecode", "", false)); len(r) != 1 || r[0].Label != mcpLabel {
		t.Errorf("claudecode rows = %+v; its published kit has gateway hooks, so the mcp row must show", r)
	}
	for _, target := range []string{"hermes"} {
		if r := applicableRows(mcpRow(target, "", false)); len(r) != 0 {
			t.Errorf("%s rows = %+v; no published kit, nothing to turn on", target, r)
		}
	}
}

func TestTheMCPRowDefaultsOffAndShowsTheRememberedAnswer(t *testing.T) {
	if r := mcpRow("claudecode", "", false); r.Options[r.Selected] != mcpOff || r.Locked {
		t.Errorf("no answer yet: selected %q locked %v; want off, open", r.Options[r.Selected], r.Locked)
	}
	if r := mcpRow("claudecode", mcpOn, false); r.Options[r.Selected] != mcpOn {
		t.Errorf("remembered on: selected %q", r.Options[r.Selected])
	}
	if r := mcpRow("claudecode", mcpOn, true); !r.Locked || r.Reason == "" {
		t.Errorf("PROVEO_SBX_MCP set: locked %v reason %q; the env decides and the row says so", r.Locked, r.Reason)
	}
}

func TestTheEnvOutranksTheRememberedMCPAnswer(t *testing.T) {
	p := Params{MCP: mcpOff, MCPSet: true}
	p.seedFromCache(agentsettings.Choice{MCP: mcpOn}, func(string) string { return "" }, false)
	if p.mcpAllowed() {
		t.Error("PROVEO_SBX_MCP=off was overridden by the cache")
	}
	q := Params{}
	q.seedFromCache(agentsettings.Choice{MCP: mcpOn}, func(string) string { return "" }, false)
	if !q.mcpAllowed() {
		t.Error("the remembered on was dropped")
	}
}

func TestAHarnessThatAlwaysTakesTheGatewayAlwaysShowsIt(t *testing.T) {
	f := &choiceui.Form{}
	if got := topologyWith("hermes", "broker", true)(f, -1).HopNote; got != "+ mcp" {
		t.Errorf("hermes hop note = %q, want + mcp with no row to read", got)
	}
	if got := topologyWith("codex", "broker", false)(f, -1).HopNote; got != "" {
		t.Errorf("codex hop note = %q, want none", got)
	}
}
