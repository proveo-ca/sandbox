// SPEC: _spec/internal/sbx/sbx-kit-v3.puml (UPSTREAM KITS)
package sbx

import (
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func upstreamKit(t *testing.T, target string, o UpstreamOptions) UpstreamKit {
	t.Helper()
	k, ok, err := UpstreamKitFor(target, o)
	if err != nil || !ok {
		t.Fatalf("UpstreamKitFor(%s) = ok %v, err %v", target, ok, err)
	}
	return k
}

func hooksOf(k UpstreamKit) []map[string]any {
	var out []map[string]any
	for _, c := range k.Capabilities {
		if c.Type != CapLifecycle {
			continue
		}
		cfg, _ := c.Config.(map[string]any)
		for _, phase := range []string{"install", "startup"} {
			hooks, _ := cfg[phase].([]any)
			for _, h := range hooks {
				hm, _ := h.(map[string]any)
				out = append(out, hm)
			}
		}
	}
	return out
}

func TestEveryBuiltinHarnessCopiesItsPublishedKitsLaunch(t *testing.T) {
	t.Parallel()
	for target, want := range map[string][]string{
		"claudecode": {"claude", "--dangerously-skip-permissions"},
		"codex":      {"codex", "--dangerously-bypass-approvals-and-sandbox"},
		"cursor":     {"cursor-agent", "--yolo"},
		"opencode":   {"opencode"},
	} {
		if got := upstreamKit(t, target, UpstreamOptions{}).Launch; !slices.Equal(got, want) {
			t.Errorf("%s launch = %q, want %q (argv0 by its PATH name, so proveo's shim gates the seed)", target, got, want)
		}
	}
	for _, target := range []string{"cecli", "hermes"} {
		if _, ok, _ := UpstreamKitFor(target, UpstreamOptions{}); ok {
			t.Errorf("%s has no published kit, yet one was found", target)
		}
	}
}

func TestUpstreamBehaviourEnvIsCopiedAndInstallLayoutIsNot(t *testing.T) {
	t.Parallel()
	claude := upstreamKit(t, "claudecode", UpstreamOptions{}).Env
	for _, want := range []string{"IS_SANDBOX=1", "DISABLE_TELEMETRY=1", "CLAUDE_ENV_FILE=/etc/sandbox-persistent.sh"} {
		if !slices.Contains(claude, want) {
			t.Errorf("claude env %q lacks %s", claude, want)
		}
	}
	if !slices.Contains(upstreamKit(t, "cursor", UpstreamOptions{}).Env, "AGENT_CLI_CREDENTIAL_STORE=memory") {
		t.Error("cursor lost AGENT_CLI_CREDENTIAL_STORE=memory")
	}
	for _, target := range []string{"codex", "opencode"} {
		for _, kv := range upstreamKit(t, target, UpstreamOptions{}).Env {
			k, _, _ := strings.Cut(kv, "=")
			if slices.Contains(upstreamLayoutEnv, k) {
				t.Errorf("%s copied %s, which describes the upstream image's install layout", target, kv)
			}
		}
	}
}

func TestTheClaudeJSONOverwriteHookIsTheOneUpstreamHookProveoSkips(t *testing.T) {
	t.Parallel()
	raw, err := upstreamFS.ReadFile("third_party/claude.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), upstreamSkippedHook) {
		t.Fatalf("vendored claude.yaml no longer has a hook described %q — re-check P6 before re-vendoring", upstreamSkippedHook)
	}
	for _, h := range hooksOf(upstreamKit(t, "claudecode", UpstreamOptions{MCPGateway: true})) {
		if d, _ := h["description"].(string); strings.HasPrefix(d, upstreamSkippedHook) {
			t.Errorf("the `> ~/.claude.json` hook was copied; P6 measured it wiping the persisted host copy")
		}
	}
	k := upstreamKit(t, "claudecode", UpstreamOptions{})
	if slices.ContainsFunc(k.Capabilities, func(c CapabilityV3) bool { return c.Type == CapSbx }) {
		t.Error("sbx@1 was copied; proveo's workload declares its own")
	}
	if !slices.ContainsFunc(k.Capabilities, func(c CapabilityV3) bool {
		m, _ := c.Config.(map[string]any)
		return c.Type == CapCredential && m["service"] == "anthropic"
	}) {
		t.Error("claude's credential@1 (service anthropic) was not copied")
	}
}

func TestMCPGatewayHooksAreCopiedOnlyWhenTheOperatorTurnsThemOn(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"claudecode", "codex", "opencode"} {
		count := func(o UpstreamOptions) int {
			n := 0
			for _, h := range hooksOf(upstreamKit(t, target, o)) {
				b, _ := yaml.Marshal(h)
				if strings.Contains(string(b), MCPGatewayEnv) {
					n++
				}
			}
			return n
		}
		if off := count(UpstreamOptions{}); off != 0 {
			t.Errorf("%s: %d gateway hooks copied with the gateway off", target, off)
		}
		if on := count(UpstreamOptions{MCPGateway: true}); on == 0 {
			t.Errorf("%s: no gateway hook copied with the gateway on", target)
		}
	}
}
