package main

import (
	"runtime"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
)

func TestPrintSubscriptionAuthHints(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	man := manifest.Manifest{Name: "claudecode", Subscription: true}
	missing := []manifest.EnvVar{{
		Name:        "CLAUDE_CODE_OAUTH_TOKEN",
		Description: "Claude Code OAuth token",
		Secret:      true,
	}}
	var out strings.Builder
	printSubscriptionAuthHints(man, missing, &out)
	got := out.String()
	for _, want := range []string{
		"CLAUDE_CODE_OAUTH_TOKEN",
		"claude setup-token",
		".env",
		"export CLAUDE_CODE_OAUTH_TOKEN=",
		"SAFE host location",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("hint output missing %q; got:\n%s", want, got)
		}
	}
	if runtime.GOOS == "linux" && !strings.Contains(got, ".zshrc") {
		t.Errorf("expected zshrc path on linux, got:\n%s", got)
	}
}

func TestPrintSubscriptionAuthHintsCursorAndVariants(t *testing.T) {
	t.Setenv("SHELL", "/bin/bash")
	tests := []struct {
		harness string
		env     string
		want    []string
	}{
		{"cursor", "CURSOR_API_KEY", []string{"CURSOR_API_KEY", "agent login", "cursor.com/dashboard", "export CURSOR_API_KEY="}},
		{"claudecode-solidity", "CLAUDE_CODE_OAUTH_TOKEN", []string{"CLAUDE_CODE_OAUTH_TOKEN", "claude setup-token"}},
	}
	for _, tc := range tests {
		t.Run(tc.harness, func(t *testing.T) {
			var out strings.Builder
			printSubscriptionAuthHints(
				manifest.Manifest{Name: tc.harness, Subscription: true},
				[]manifest.EnvVar{{Name: tc.env, Secret: true}},
				&out,
			)
			got := out.String()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("%s hint missing %q; got:\n%s", tc.harness, want, got)
				}
			}
		})
	}
}

func TestPrintSubscriptionAuthHintsEmpty(t *testing.T) {
	var out strings.Builder
	printSubscriptionAuthHints(manifest.Manifest{Name: "claudecode"}, nil, &out)
	if out.Len() != 0 {
		t.Errorf("empty missing should print nothing, got %q", out.String())
	}
}

func TestHarnessFamily(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
	}{
		{"claudecode", "claudecode"},
		{"claudecode-solidity", "claudecode"},
		{"claudecode-browser", "claudecode"},
		{"cursor-browser", "cursor"},
		{"opencode", "opencode"},
		{"unknown", "unknown"},
	}
	for _, tc := range tests {
		if got := harnessFamily(tc.in); got != tc.want {
			t.Errorf("harnessFamily(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFishExportInHints(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/fish")
	var out strings.Builder
	printSubscriptionAuthHints(
		manifest.Manifest{Name: "cursor"},
		[]manifest.EnvVar{{Name: "CURSOR_API_KEY", Secret: true}},
		&out,
	)
	if !strings.Contains(out.String(), `set -gx CURSOR_API_KEY`) {
		t.Errorf("fish hint should use set -gx; got:\n%s", out.String())
	}
}
