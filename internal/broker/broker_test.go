package broker

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func newRequest(t *testing.T, method, rawurl string, headers map[string]string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, rawurl, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestApply(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		cfg         Config
		url         string
		reqHeaders  map[string]string
		wantHeaders http.Header // full expected header set after Apply
	}{
		{
			name:        "inject on provider host replaces sentinel",
			cfg:         Config{Routes: []Route{{Provider: "anthropic", Hosts: []string{".anthropic.com"}, Header: "x-api-key", Value: "sk-ant-REAL"}}},
			url:         "https://api.anthropic.com/v1/messages",
			reqHeaders:  map[string]string{"x-api-key": "sentinel"},
			wantHeaders: http.Header{"X-Api-Key": {"sk-ant-REAL"}},
		},
		{
			name:        "strip credentials off-provider, keep others",
			cfg:         Config{Routes: []Route{{Provider: "anthropic", Hosts: []string{".anthropic.com"}, Header: "x-api-key", Value: "sk-ant-REAL"}}},
			url:         "https://evil.example.com/collect",
			reqHeaders:  map[string]string{"x-api-key": "sk-ant-REAL", "authorization": "Bearer x", "content-type": "application/json"},
			wantHeaders: http.Header{"Content-Type": {"application/json"}},
		},
		{
			name:        "pass provider auth through when no value to inject",
			cfg:         Config{Routes: []Route{{Provider: "anthropic", Hosts: []string{".anthropic.com"}, Header: "x-api-key"}}, Strip: DefaultStripHeaders},
			url:         "https://api.anthropic.com/v1/messages",
			reqHeaders:  map[string]string{"x-api-key": "agents-own-key"},
			wantHeaders: http.Header{"X-Api-Key": {"agents-own-key"}},
		},
		{
			name:        "inert broker leaves request untouched",
			cfg:         Config{},
			url:         "https://evil.com/x",
			reqHeaders:  map[string]string{"authorization": "Bearer secret"},
			wantHeaders: http.Header{"Authorization": {"Bearer secret"}},
		},
		{
			name:        "default strip applied off-provider when injecting",
			cfg:         Config{Routes: []Route{{Provider: "anthropic", Hosts: []string{".anthropic.com"}, Header: "x-api-key", Value: "K"}}},
			url:         "https://evil.com/x",
			reqHeaders:  map[string]string{"x-goog-api-key": "leak"},
			wantHeaders: http.Header{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := New(tc.cfg)
			if err != nil {
				t.Fatalf("New(%+v): %v", tc.cfg, err)
			}
			req := newRequest(t, "POST", tc.url, tc.reqHeaders)
			b.Apply(req)
			if diff := cmp.Diff(tc.wantHeaders, req.Header); diff != "" {
				t.Errorf("Apply(%s) headers mismatch (-want +got):\n%s", tc.url, diff)
			}
		})
	}
}

func TestApplyInjectsQueryParam(t *testing.T) {
	t.Parallel()
	b, err := New(Config{Routes: []Route{{Provider: "google", Hosts: []string{"generativelanguage.googleapis.com"}, Query: "key", Value: "GKEY"}}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := newRequest(t, "POST", "https://generativelanguage.googleapis.com/v1/models?key=sentinel&x=1", nil)
	b.Apply(req)
	if got, want := req.URL.Query().Get("key"), "GKEY"; got != want {
		t.Errorf("Apply query key = %q, want %q", got, want)
	}
	if got, want := req.URL.Query().Get("x"), "1"; got != want {
		t.Errorf("Apply clobbered unrelated query x = %q, want %q", got, want)
	}
}

func TestIsProviderHost(t *testing.T) {
	t.Parallel()
	b, _ := New(Config{Routes: []Route{{Provider: "anthropic", Hosts: []string{".anthropic.com", "openrouter.ai"}}}, Strip: []string{"authorization"}})
	tests := []struct {
		host string
		want bool
	}{
		{"api.anthropic.com", true},
		{"anthropic.com", true},
		{"deep.sub.anthropic.com", true},
		{"openrouter.ai", true},
		{"notanthropic.com", false},
		{"anthropic.com.evil.com", false},
		{"evil.com", false},
	}
	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			t.Parallel()
			if got := b.onRoute(tc.host); got != tc.want {
				t.Errorf("isProviderHost(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

func TestApplyRecognizesHostFromPortAndProxyShape(t *testing.T) {
	t.Parallel()
	b, _ := New(Config{Routes: []Route{{Provider: "anthropic", Hosts: []string{".anthropic.com"}, Header: "x-api-key", Value: "K"}}})

	withPort := newRequest(t, "POST", "https://api.anthropic.com:443/v1/messages", nil)
	b.Apply(withPort)
	if got := withPort.Header.Get("x-api-key"); got != "K" {
		t.Errorf("Apply(host:port) x-api-key = %q, want K", got)
	}

	u, err := url.Parse("https://api.anthropic.com/v1")
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	proxyShape := &http.Request{Method: "POST", Host: "api.anthropic.com", Header: http.Header{}, URL: u}
	b.Apply(proxyShape)
	if got := proxyShape.Header.Get("x-api-key"); got != "K" {
		t.Errorf("Apply(req.Host shape) x-api-key = %q, want K", got)
	}
}

func TestNewReadsValueFileTrimmingNewline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	vf := filepath.Join(dir, "credential")
	if err := os.WriteFile(vf, []byte("sk-ant-REAL\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	b, err := New(Config{Routes: []Route{{Provider: "anthropic", Hosts: []string{".anthropic.com"}, Header: "x-api-key", ValueFile: vf}}})
	if err != nil {
		t.Fatalf("New(ValueFile): %v", err)
	}
	if !b.InjectReady() {
		t.Fatal("New(ValueFile) InjectReady() = false, want true")
	}
	req := newRequest(t, "POST", "https://api.anthropic.com/v1", nil)
	b.Apply(req)
	if got, want := req.Header.Get("x-api-key"), "sk-ant-REAL"; got != want {
		t.Errorf("injected value = %q, want %q (trailing newline must be trimmed)", got, want)
	}
}

// A provider configured with no injectable value must still STRIP off-provider
// (degrade to strip + pass-through), not go inert. Regression guard for S3.
func TestBrokerStripsWithoutValue(t *testing.T) {
	b, err := New(Config{Routes: []Route{{Provider: "anthropic", Hosts: []string{".anthropic.com"}, Header: "x-api-key"}}}) // no Value
	if err != nil {
		t.Fatal(err)
	}
	if b.InjectReady() {
		t.Fatal("no value => must not be inject-ready")
	}
	if !b.Active() {
		t.Fatal("provider configured => broker must be active (stripping), not inert")
	}
	off, _ := http.NewRequest("GET", "https://evil.com/", nil)
	off.Header.Set("authorization", "Bearer stolen")
	b.Apply(off)
	if off.Header.Get("authorization") != "" {
		t.Error("off-provider credential header must be stripped even with no inject value")
	}
	on, _ := http.NewRequest("GET", "https://api.anthropic.com/v1/messages", nil)
	on.Header.Set("x-api-key", "agents-own-key")
	b.Apply(on)
	if on.Header.Get("x-api-key") != "agents-own-key" {
		t.Error("on-provider request must pass the agent's own credential through untouched")
	}
}

// TestMultiProviderRoutes is the regression for the failure the route table
// exists to fix: a session whose roles span vendors — ARCHITECT_MODEL on
// moonshot, EDITOR_MODEL on xai — needs both keys injected in one run. With a
// single pinned provider one of them received the "proveo-brokered" sentinel and
// the harness reported an invalid API key.
func TestMultiProviderRoutes(t *testing.T) {
	b, err := New(Config{Routes: []Route{
		{Provider: "moonshot", Hosts: []string{".moonshot.ai", ".kimi.com"}, Header: "authorization", Value: "Bearer sk-moon"},
		{Provider: "xai", Hosts: []string{".x.ai"}, Header: "authorization", Value: "Bearer sk-xai"},
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := b.InjectingProviders(); len(got) != 2 {
		t.Fatalf("both providers must inject; got %v", got)
	}

	for _, tc := range []struct{ host, want string }{
		{"api.moonshot.ai", "Bearer sk-moon"},
		{"api.kimi.com", "Bearer sk-moon"}, // second suffix of the same route
		{"api.x.ai", "Bearer sk-xai"},
	} {
		req, _ := http.NewRequest("POST", "https://"+tc.host+"/v1/chat", nil)
		b.Apply(req)
		if got := req.Header.Get("authorization"); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.host, got, tc.want)
		}
	}

	// The invariant that makes N routes no worse than one: a provider's secret is
	// never attached to another provider's request.
	req, _ := http.NewRequest("POST", "https://api.x.ai/v1/chat", nil)
	b.Apply(req)
	if strings.Contains(req.Header.Get("authorization"), "moon") {
		t.Error("moonshot's key leaked onto an xai request")
	}

	// Off-route is still stripped, with two routes as with one.
	off, _ := http.NewRequest("POST", "https://evil.example/collect", nil)
	off.Header.Set("authorization", "Bearer sk-moon")
	off.Header.Set("x-api-key", "sk-xai")
	b.Apply(off)
	if off.Header.Get("authorization") != "" || off.Header.Get("x-api-key") != "" {
		t.Errorf("off-route credentials survived: %v", off.Header)
	}
}

// TestRouteWithoutSecretPassesThrough covers the mixed case a multi-provider
// session makes common: one provider has a brokered key, another is present but
// not injectable (a signed-request provider). The injectable one must still
// inject, and the other must keep the agent's own credential rather than have it
// stripped — being on-route is what exempts it.
func TestRouteWithoutSecretPassesThrough(t *testing.T) {
	b, _ := New(Config{Routes: []Route{
		{Provider: "moonshot", Hosts: []string{".moonshot.ai"}, Header: "authorization", Value: "Bearer sk-moon"},
		{Provider: "bedrock", Hosts: []string{".amazonaws.com"}}, // detectable, not injectable
	}})
	req, _ := http.NewRequest("POST", "https://bedrock-runtime.us-east-1.amazonaws.com/model", nil)
	req.Header.Set("authorization", "AWS4-HMAC-SHA256 Credential=...")
	b.Apply(req)
	if !strings.HasPrefix(req.Header.Get("authorization"), "AWS4-HMAC") {
		t.Errorf("a non-injectable route must pass the agent's own credential through; got %q",
			req.Header.Get("authorization"))
	}
}
