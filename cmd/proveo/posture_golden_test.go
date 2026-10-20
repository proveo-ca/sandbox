// SPEC: _spec/internal/posture/one-value-two-renderings.puml
package main

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/posture"
)

func TestPostureGolden(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    posture.Posture
	}{
		{"posture-sbx", posture.Posture{
			Target: "claudecode", Credentials: "broker (sbx proxy)",
			AddOns: "browser,docker (sandbox)", AgentEvidence: "verbose",
			DetectedKeys: "anthropic", AuthVar: "CLAUDE_CODE_OAUTH_TOKEN", LocalModel: "",
			Observability: posture.Observability(),
			EnforcedBy:    posture.EnforcedBy(),
			Image:         posture.Image("proveo/claudecode:local"),
			ModelRoles:    "main=claude-opus-5 (anthropic)", RoleProviders: "anthropic",
			MCPGateway: posture.MCPGateway(false, "MCP_GATEWAY_URL"),
			Workspace:  posture.Workspace(false),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			section(&b, "resolved posture")
			b.WriteString(tc.p.Render())
			assertGolden(t, tc.name, b.String())
		})
	}
}

func TestPostureRendersEveryRowItDeclares(t *testing.T) {
	t.Parallel()
	full := posture.Posture{
		Target: "t", Credentials: "c", AddOns: "a", AgentEvidence: "v",
		DetectedKeys: "d", AuthVar: "av", LocalModel: "lm", Observability: "o", EnforcedBy: "eb",
		Image: "i", ModelRoles: "mr", RoleProviders: "rp", MCPGateway: "mg", Workspace: "w",
	}
	if got := full.Render(); strings.Contains(got, "(unset)") {
		t.Errorf("a fully-populated posture still rendered an unset row:\n%s", got)
	}
	if n := strings.Count(full.Render(), "\n"); n != 14 {
		t.Errorf("Render wrote %d rows, want 14 — a field was added to Posture without a row, or vice versa", n)
	}
}
