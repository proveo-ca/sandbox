package sandbox

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/sbx"
)

func upstreamOpenAI() sbx.CapabilityV3 {
	return sbx.CapabilityV3{Type: sbx.CapCredential, Optional: true, Config: map[string]any{
		"service": "openai",
		"phase":   "runtime",
		"apiKey": map[string]any{"name": "OPENAI_API_KEY", "inject": []any{
			map[string]any{"domain": "api.openai.com", "header": "Authorization"},
		}},
		"oauth": map[string]any{
			"tokenEndpoint": map[string]any{"host": "auth.openai.com", "path": "/oauth/token"},
			"resourceHosts": []any{"chatgpt.com"},
		},
	}}
}

// SPEC: _spec/internal/sbx/credential-resolution.puml (R9)
func TestDeclaredCredentialsReadUpstreamMapsAndProveoStructsAlike(t *testing.T) {
	t.Parallel()
	caps := []sbx.CapabilityV3{
		{Type: sbx.CapSbx},
		upstreamOpenAI(),
		{Type: sbx.CapCredential, Optional: true, Config: sbx.CredentialV3{Service: "opencode", Phase: "runtime",
			APIKey: &sbx.APIKeyV3{Name: "OPENCODE_API_KEY", ProxyManaged: true,
				Inject: []sbx.CredInjectV3{{Domain: "opencode.ai", Header: "Authorization"}}}}},
	}
	got := declaredCredentials(caps)
	want := []declaredCredential{
		{Service: "openai", APIKeyHosts: []string{"api.openai.com"}, OAuthHosts: []string{"auth.openai.com", "chatgpt.com"}},
		{Service: "opencode", APIKeyHosts: []string{"opencode.ai"}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("declaredCredentials mismatch (-want +got):\n%s", diff)
	}
}

func TestChosenServiceFollowsTheAuthVarElseTheSubscription(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		target, authVar string
		sub             bool
		want            string
	}{
		{"opencode", "OPENCODE_API_KEY", true, "opencode"},
		{"codex", "", true, "openai"},
		{"claudecode", "", true, "anthropic"},
		{"cursor", "", true, "cursor"},
		{"opencode", "", true, ""},
		{"hermes", "", false, ""},
	} {
		if got := chosenService(tc.target, tc.authVar, tc.sub); got != tc.want {
			t.Errorf("chosenService(%q, %q, %v) = %q, want %q", tc.target, tc.authVar, tc.sub, got, tc.want)
		}
	}
}

// SPEC: _spec/internal/sbx/credential-resolution.puml (R9)
func TestAnUnattendedCreateRefusesWhenItsOwnCredentialIsUnbound(t *testing.T) {
	t.Parallel()
	states := resolveCredentials(declaredCredentials([]sbx.CapabilityV3{upstreamOpenAI()}),
		map[string]sbx.StoreEntry{"openai": {APIKey: true}}, map[string]sbx.Binding{})
	err := unboundRefusal(states, "openai", true, false)
	if err == nil {
		t.Fatal("unattended create with the run's API key unapproved: want a refusal")
	}
	for _, want := range []string{"openai's stored API key has no approval", "Run once in a terminal",
		"    openai:\n      apiKey:\n        domains: [api.openai.com]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q:\n%s", want, err)
		}
	}
	oauth := resolveCredentials(declaredCredentials([]sbx.CapabilityV3{upstreamOpenAI()}),
		map[string]sbx.StoreEntry{"openai": {APIKey: true, OAuth: true}}, map[string]sbx.Binding{})
	if err := unboundRefusal(oauth, "openai", true, false); err != nil {
		t.Errorf("an unapproved key with a stored OAuth login falls back to OAuth; unboundRefusal = %v, want nil", err)
	}
	for _, tc := range []struct {
		name                 string
		chosen               string
		created, interactive bool
	}{
		{"a terminal lets sbx ask", "openai", true, true},
		{"a reattach creates nothing", "openai", false, false},
		{"another service is not the run's", "xai", true, false},
		{"no chosen service", "", true, false},
	} {
		if err := unboundRefusal(states, tc.chosen, tc.created, tc.interactive); err != nil {
			t.Errorf("%s: unboundRefusal = %v, want nil", tc.name, err)
		}
	}
}

// SPEC: _spec/internal/sbx/credential-resolution.puml (R10)
func TestCredentialInUseNamesAnAgentOwnedLoginThatOutranksSbx(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		target, out, want string
	}{
		{"opencode", `[{"id":"cred_17f8","integrationID":"openai","active":true,"value":{"methodID":"chatgpt-token-sharing"}}]`,
			"opencode uses openai: an agent-owned login (chatgpt-token-sharing) — it outranks sbx's credential"},
		{"opencode", `[{"id":"sbx-openai","integrationID":"openai","active":true,"value":{"methodID":"chatgpt-browser"}},` +
			`{"id":"cred_old","integrationID":"openai","active":false,"value":{"methodID":"chatgpt-token-sharing"}}]`,
			"opencode uses openai: sbx's credential (sbx-openai)"},
		{"opencode", `[{"id":"cred_mig","integrationID":"openai","label":"OAuth","active":true,` +
			`"value":{"methodID":"chatgpt-browser","access":"oai-oat01-proxy-managed"}}]`,
			"opencode uses openai: sbx's credential (cred_mig)"},
		{"opencode", `[]`, "opencode holds no active credential"},
		{"codex", `{"OPENAI_API_KEY": "proxy-managed"}`, "codex uses sbx's credential (sentinel in its credential file)"},
		{"claudecode", `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-real"}}`, "claudecode uses a credential it holds itself, not sbx's sentinel"},
		{"codex", "", ""},
		{"hermes", `{}`, ""},
	} {
		if got := credentialInUse(tc.target, tc.out); got != tc.want {
			t.Errorf("credentialInUse(%s, %.40s…) = %q, want %q", tc.target, tc.out, got, tc.want)
		}
	}
}

// SPEC: _spec/internal/sbx/credential-resolution.puml (R10)
func TestAnExpiredSbxLoginIsNamedWithItsService(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_794_000_000_000)
	past, future := now.Add(-time.Hour).UnixMilli(), now.Add(time.Hour).UnixMilli()
	rec := func(id string, active bool, access string, expires int64) string {
		return fmt.Sprintf(`{"id":%q,"integrationID":"openai","active":%v,"value":{"methodID":"chatgpt-browser","access":%q,"expires":%d}}`,
			id, active, access, expires)
	}
	for _, tc := range []struct {
		name, target, out, want string
	}{
		{"sbx record past its expiry", "opencode", "[" + rec("sbx-openai", true, "oai-oat01-proxy-managed", past) + "]", "openai"},
		{"expiry in seconds", "opencode", "[" + rec("sbx-openai", true, "oai-oat01-proxy-managed", now.Add(-time.Hour).Unix()) + "]", "openai"},
		{"migrated record holding the sentinel", "opencode", "[" + rec("cred_mig", true, "oai-oat01-proxy-managed", past) + "]", "openai"},
		{"still valid", "opencode", "[" + rec("sbx-openai", true, "oai-oat01-proxy-managed", future) + "]", ""},
		{"inactive", "opencode", "[" + rec("sbx-openai", false, "oai-oat01-proxy-managed", past) + "]", ""},
		{"agent-owned login", "opencode", "[" + rec("cred_native", true, "eyJreal", past) + "]", ""},
		{"no expiry recorded", "opencode", "[" + rec("sbx-github-copilot", true, "gho_x", 0) + "]", ""},
		{"not opencode", "codex", "[" + rec("sbx-openai", true, "oai-oat01-proxy-managed", past) + "]", ""},
		{"unreadable", "opencode", "not json", ""},
		{"one odd expiry hides no other record", "opencode", `[{"id":"sbx-github-copilot","integrationID":"github-copilot","active":true,` +
			`"value":{"access":"gho_x","expires":"soon"}},` + rec("sbx-openai", true, "oai-oat01-proxy-managed", past) + "]", "openai"},
		{"no integration id", "opencode", `[{"id":"sbx-openai","active":true,"value":{"access":"oai-oat01-proxy-managed","expires":` +
			fmt.Sprint(past) + `}}]`, "openai"},
	} {
		svc, at := expiredSbxLogin(tc.target, tc.out, now)
		if svc != tc.want {
			t.Errorf("%s: service = %q, want %q", tc.name, svc, tc.want)
		}
		if svc != "" && !at.Before(now) {
			t.Errorf("%s: expiry %v is not in the past", tc.name, at)
		}
	}
}
