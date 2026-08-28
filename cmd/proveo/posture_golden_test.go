// SPEC: _spec/internal/posture/one-value-two-renderings.puml
package main

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/posture"
)

// The run-log posture block is what an operator reads AFTER a failure, and until
// now nothing asserted it. runlog sorts the keys, so the order was already stable;
// what was unpinned is the SET and the WORDING — a row could appear, vanish or
// change its sentence and no test would notice.
//
// One golden per backend, because the two differ in exactly the places that have
// been wrong before: who enforces egress, what evidence exists, and whether an sbx
// MCP gateway is even a question.
func TestPostureGolden(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    posture.Posture
	}{
		{"posture-sbx", posture.Posture{
			Target: "claudecode", EgressTier: "allowlist", Credentials: "forward",
			AddOns: "browser,docker (sandbox)", AgentEvidence: "verbose",
			DetectedKeys: "anthropic", Brokered: "", ReachableHosts: ".anthropic.com",
			HarnessHosts: "", AuthVar: "CLAUDE_CODE_OAUTH_TOKEN", LocalModel: "",
			Observability: posture.Observability("allowlist", "forward", true),
			EnforcedBy:    posture.EnforcedBy(true),
			Image:         posture.Image("proveo/claudecode:local"),
			ModelRoles:    "main=claude-opus-5 (anthropic)", RoleProviders: "anthropic",
			MCPGateway: posture.MCPGateway(true, false, "MCP_GATEWAY_URL"),
			Workspace:  posture.Workspace(false),
		}},
		{"posture-docker", posture.Posture{
			Target: "claudecode", EgressTier: "open", Credentials: "broker",
			AddOns: "", AgentEvidence: "default",
			DetectedKeys: "anthropic,openai", Brokered: "anthropic", ReachableHosts: "",
			HarnessHosts: "example.test", AuthVar: "", LocalModel: "qwen2.5-coder",
			Observability: posture.Observability("open", "broker", false),
			EnforcedBy:    posture.EnforcedBy(false),
			Image:         posture.Image("proveo/claudecode:latest"),
			ModelRoles:    "main=claude-opus-5 (anthropic)", RoleProviders: "anthropic",
			MCPGateway: posture.MCPGateway(false, false, "MCP_GATEWAY_URL"),
			Workspace:  posture.Workspace(true),
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

// The header and the run-log block are rendered from ONE value. This pins the
// property the split was made for: every row the block reports is non-empty for a
// fully-resolved run, so a field added to Posture and left unset fails here rather
// than printing "(unset)" to an operator mid-incident.
func TestPostureRendersEveryRowItDeclares(t *testing.T) {
	t.Parallel()
	full := posture.Posture{
		Target: "t", EgressTier: "e", Credentials: "c", AddOns: "a", AgentEvidence: "v",
		DetectedKeys: "d", Brokered: "b", ReachableHosts: "r", HarnessHosts: "h",
		AuthVar: "av", LocalModel: "lm", Observability: "o", EnforcedBy: "eb",
		Image: "i", ModelRoles: "mr", RoleProviders: "rp", MCPGateway: "mg", Workspace: "w",
	}
	if got := full.Render(); strings.Contains(got, "(unset)") {
		t.Errorf("a fully-populated posture still rendered an unset row:\n%s", got)
	}
	if n := strings.Count(full.Render(), "\n"); n != 18 {
		t.Errorf("Render wrote %d rows, want 18 — a field was added to Posture without a row, or vice versa", n)
	}
}
