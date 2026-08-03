package provider

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func lookupFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDetect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{name: "cursor api key", env: map[string]string{"CURSOR_API_KEY": "sk"}, want: []string{"cursor"}},
		{name: "none", env: nil, want: nil},
		{name: "anthropic api key", env: map[string]string{"ANTHROPIC_API_KEY": "x"}, want: []string{"anthropic"}},
		{name: "anthropic oauth alias", env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "t"}, want: []string{"anthropic"}},
		{name: "google alias GOOGLE_API_KEY", env: map[string]string{"GOOGLE_API_KEY": "g"}, want: []string{"google"}},
		{name: "moonshot api key", env: map[string]string{"MOONSHOT_API_KEY": "m"}, want: []string{"moonshot"}},
		{name: "bedrock via AWS creds", env: map[string]string{"AWS_ACCESS_KEY_ID": "a"}, want: []string{"bedrock"}},
		{name: "vertex via app creds", env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "/p"}, want: []string{"vertex"}},
		{
			name: "union preserves registry order",
			env:  map[string]string{"OPENAI_API_KEY": "x", "ANTHROPIC_API_KEY": "y", "GROQ_API_KEY": "z"},
			want: []string{"anthropic", "openai", "groq"},
		},
		{
			name: "cursor before openai when both present",
			env:  map[string]string{"CURSOR_API_KEY": "c", "OPENAI_API_KEY": "o"},
			want: []string{"cursor", "openai"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Detect(lookupFrom(tc.env))
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Detect(%v) mismatch (-want +got):\n%s", tc.env, diff)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		provider string
		env      map[string]string
		wantOK   bool
		want     Resolved
	}{
		{
			name: "anthropic prefers api key over oauth", provider: "anthropic",
			env:    map[string]string{"ANTHROPIC_API_KEY": "sk-ant", "CLAUDE_CODE_OAUTH_TOKEN": "oauth"},
			wantOK: true,
			want:   Resolved{Hosts: []string{".anthropic.com"}, Header: "x-api-key", Value: "sk-ant", EnvVar: "ANTHROPIC_API_KEY"},
		},
		{
			name: "anthropic oauth fallback is bearer", provider: "anthropic",
			env:    map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "oauth"},
			wantOK: true,
			want:   Resolved{Hosts: []string{".anthropic.com"}, Header: "authorization", Value: "Bearer oauth", EnvVar: "CLAUDE_CODE_OAUTH_TOKEN"},
		},
		{
			name: "openai is bearer", provider: "openai",
			env:    map[string]string{"OPENAI_API_KEY": "sk-o"},
			wantOK: true,
			want:   Resolved{Hosts: []string{".openai.com"}, Header: "authorization", Value: "Bearer sk-o", EnvVar: "OPENAI_API_KEY"},
		},
		{
			name: "moonshot is bearer", provider: "moonshot",
			env:    map[string]string{"MOONSHOT_API_KEY": "sk-m"},
			wantOK: true,
			want:   Resolved{Hosts: []string{".moonshot.ai", ".kimi.com"}, Header: "authorization", Value: "Bearer sk-m", EnvVar: "MOONSHOT_API_KEY"},
		},
		{
			name: "google uses header not bearer", provider: "google",
			env:    map[string]string{"GEMINI_API_KEY": "g"},
			wantOK: true,
			want:   Resolved{Hosts: []string{"generativelanguage.googleapis.com"}, Header: "x-goog-api-key", Value: "g", EnvVar: "GEMINI_API_KEY"},
		},
		{
			name: "known but no key: hosts set, value empty", provider: "anthropic",
			env: nil, wantOK: true,
			want: Resolved{Hosts: []string{".anthropic.com"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Resolve(tc.provider, lookupFrom(tc.env))
			if ok != tc.wantOK {
				t.Fatalf("Resolve(%q, %v) ok = %v, want %v", tc.provider, tc.env, ok, tc.wantOK)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Resolve(%q, %v) mismatch (-want +got):\n%s", tc.provider, tc.env, diff)
			}
		})
	}
}

func TestResolveNotInjectable(t *testing.T) {
	t.Parallel()
	// Signed-request providers are detectable but must NOT be broker-injectable.
	tests := []struct {
		provider string
		env      map[string]string
	}{
		{"bedrock", map[string]string{"AWS_ACCESS_KEY_ID": "x"}},
		{"azure", map[string]string{"AZURE_API_KEY": "x"}},
		{"vertex", map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "/p"}},
		{"nonsense", nil},
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			t.Parallel()
			if _, ok := Resolve(tc.provider, lookupFrom(tc.env)); ok {
				t.Errorf("Resolve(%q) ok = true, want false (not broker-injectable)", tc.provider)
			}
		})
	}
}

func TestACLBody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		provider string
		want     string
		wantOK   bool
	}{
		{"anthropic", "dstdomain .anthropic.com", true},
		{"moonshot", "dstdomain .moonshot.ai .kimi.com", true},
		{"bedrock", `dstdom_regex (^|\.)bedrock-runtime\.[a-z0-9-]+\.amazonaws\.com$`, true},
		{"nonsense", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			t.Parallel()
			got, ok := ACLBody(tc.provider)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("ACLBody(%q) = (%q, %v), want (%q, %v)", tc.provider, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestKeyVarsCoversInjectableProviders(t *testing.T) {
	t.Parallel()
	got := KeyVars()
	have := make(map[string]bool, len(got))
	for _, k := range got {
		have[k] = true
	}
	for _, want := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY", "MOONSHOT_API_KEY", "GEMINI_API_KEY", "CURSOR_API_KEY"} {
		if !have[want] {
			t.Errorf("KeyVars() = %v, missing %q", got, want)
		}
	}
}
