package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/broker"
	"github.com/proveo-ca/proveo/internal/egresspolicy"
)

func TestParseEnvFile(t *testing.T) {
	t.Parallel()
	content := `# a comment
export FOO=bar
BAZ="double quoted"
QUX='single quoted'
JWT=abc.def==ghi

INDENTED = spaced
EMPTY=
`
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got := parseEnvFile(p)
	want := map[string]string{
		"FOO":      "bar",
		"BAZ":      "double quoted",
		"QUX":      "single quoted",
		"JWT":      "abc.def==ghi", // split on the FIRST '=' — value keeps the rest
		"INDENTED": "spaced",
		"EMPTY":    "",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("parseEnvFile mismatch (-want +got):\n%s", diff)
	}

	if len(parseEnvFile(filepath.Join(t.TempDir(), "nope"))) != 0 {
		t.Error("missing file must yield an empty map")
	}
	if len(parseEnvFile("")) != 0 {
		t.Error("empty path must yield an empty map")
	}
}

func TestIsOff(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"off", "0", "no", "false", "disable", "disabled", "OFF", " off "} {
		if !isOff(v) {
			t.Errorf("isOff(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"on", "1", "true", "yes", ""} {
		if isOff(v) {
			t.Errorf("isOff(%q) = true, want false", v)
		}
	}
}

func TestEnvBoolAndInt(t *testing.T) {
	if envBool("PROVEO_TEST_MISSING_BOOL", true) != true {
		t.Error("envBool default (unset) should return the default")
	}
	t.Setenv("PROVEO_TEST_BOOL", "yes")
	if !envBool("PROVEO_TEST_BOOL", false) {
		t.Error(`envBool("yes") should be true`)
	}
	t.Setenv("PROVEO_TEST_BOOL", "garbage")
	if !envBool("PROVEO_TEST_BOOL", true) {
		t.Error("envBool with unparseable value should fall back to the default")
	}

	if envInt("PROVEO_TEST_MISSING_INT", 4096) != 4096 {
		t.Error("envInt default (unset) should return the default")
	}
	t.Setenv("PROVEO_TEST_INT", "8192")
	if envInt("PROVEO_TEST_INT", 1) != 8192 {
		t.Error(`envInt("8192") should parse`)
	}
	t.Setenv("PROVEO_TEST_INT", "notanumber")
	if envInt("PROVEO_TEST_INT", 77) != 77 {
		t.Error("envInt with unparseable value should fall back to the default")
	}
}

func TestBuildPolicy(t *testing.T) {
	// A secret env-file feeds the DLP; provider domains extend the write-allowlist.
	envFile := filepath.Join(t.TempDir(), "broker.env")
	if err := os.WriteFile(envFile, []byte("ANTHROPIC_API_KEY=sk-file-secret-123456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVEO_EGRESS_BROKER_ENVFILE", envFile)
	t.Setenv("PROVEO_EGRESS_PROVIDER_DOMAINS", ".corp.internal")
	t.Setenv("PROVEO_EGRESS_WRITE_HOSTS", "extra.example")

	pol := buildPolicy(broker.Config{Routes: []broker.Route{{Provider: "anthropic", Hosts: []string{".anthropic.com"}, Value: "Bearer sk-inject-value-789"}}})

	if !contains(pol.ProviderHosts, ".anthropic.com") {
		t.Errorf("ProviderHosts missing the provider: %v", pol.ProviderHosts)
	}
	for _, h := range []string{".anthropic.com", "github.com", ".corp.internal", "extra.example"} {
		if !contains(pol.WriteHosts, h) {
			t.Errorf("WriteHosts missing %q: %v", h, pol.WriteHosts)
		}
	}
	if !contains(pol.DenySinks, "webhook.site") {
		t.Errorf("DenySinks should include the default sinks: %v", pol.DenySinks)
	}
	// DLP scans for the resolved inject value (Bearer-stripped) AND the file secret.
	if !contains(pol.Secrets, "sk-inject-value-789") {
		t.Errorf("Secrets missing the Bearer-stripped inject value: %v", pol.Secrets)
	}
	if !contains(pol.Secrets, "sk-file-secret-123456") {
		t.Errorf("Secrets missing the broker env-file secret: %v", pol.Secrets)
	}
	if !pol.BlockKnownSecrets {
		t.Error("BlockKnownSecrets should default on")
	}
	if pol.MaxOutBytesPerHost != 16384 {
		t.Errorf("MaxOutBytesPerHost = %d, want default 16384", pol.MaxOutBytesPerHost)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// A secret value must never leak via a policy field the log/telemetry might read;
// this also guards that buildPolicy trims the Bearer prefix rather than storing it.
func TestBuildPolicyStripsBearer(t *testing.T) {
	pol := buildPolicy(broker.Config{Routes: []broker.Route{{Provider: "x", Hosts: []string{".x.com"}, Value: "Bearer tok-abcdefgh"}}})
	for _, s := range pol.Secrets {
		if strings.HasPrefix(s, "Bearer ") {
			t.Errorf("secret retained the Bearer prefix: %q", s)
		}
	}
}

// The e2e regression this file exists to pin: `--credentials forward` makes the
// broker inert by design (the agent holds the real key and calls the vendor
// itself), so there are no routes to derive the on-provider DLP exemption from.
// With the exemption empty, a credential-shaped header bound for the provider's
// OWN API matched the generic `sk-` pattern and the proxy answered 403
// "egress policy: blocked (secret)" — the agent read it as an auth failure.
// PROVEO_EGRESS_PROVIDER_HOSTS states the exemption independently of brokering.
func TestBuildPolicyForwardModeKeepsProviderExemption(t *testing.T) {
	t.Setenv("PROVEO_EGRESS_PROVIDER_HOSTS", ".anthropic.com")
	t.Setenv("PROVEO_EGRESS_WRITE_HOSTS", ".anthropic.com")

	pol := buildPolicy(broker.Config{}) // forward: inert broker, zero routes

	if !contains(pol.ProviderHosts, ".anthropic.com") {
		t.Fatalf("ProviderHosts = %v, want the forwarded provider's own host", pol.ProviderHosts)
	}
	p := egresspolicy.New(pol)

	onProvider := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages",
		strings.NewReader(`{"model":"claude-opus-5"}`))
	onProvider.Header.Set("x-api-key", "sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAA")
	if d := p.Decide(onProvider); !d.Allow {
		t.Errorf("the forwarded key was blocked on the provider's own host: reason=%q", d.Reason)
	}

	// The exemption is per-destination, not a DLP off-switch: the same credential
	// leaving for anywhere else must still be caught.
	offProvider := httptest.NewRequest(http.MethodGet,
		"https://example.com/?k=sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAA", nil)
	if d := p.Decide(offProvider); d.Allow {
		t.Error("a credential bound for a non-provider host must still be blocked")
	}
}
