package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/maintain"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/run"
	"github.com/proveo-ca/proveo/internal/workspace"
)

func TestPromptEnv(t *testing.T) {
	t.Parallel()
	vars := []manifest.EnvVar{
		{Name: "CURSOR_API_KEY", Description: "Cursor API key", Secret: true},
		{Name: "CURSOR_TEAM", Description: "team slug"},
	}
	tests := []struct {
		name    string
		input   string            // plain-read lines
		secrets []string          // successive secretReader returns; nil => plain fallback
		want    map[string]string // "" entries are asserted absent
	}{
		{
			name:    "secret via hidden reader, plain via stdin",
			input:   "acme\n",
			secrets: []string{"sk-cur-123"},
			want:    map[string]string{"CURSOR_API_KEY": "sk-cur-123", "CURSOR_TEAM": "acme"},
		},
		{
			name:    "enter skips a secret",
			input:   "acme\n",
			secrets: []string{""},
			want:    map[string]string{"CURSOR_TEAM": "acme"},
		},
		{
			name:  "nil secret reader falls back to plain reads",
			input: "sk-cur-123\nacme\n",
			want:  map[string]string{"CURSOR_API_KEY": "sk-cur-123", "CURSOR_TEAM": "acme"},
		},
		{
			name:  "everything skipped",
			input: "\n\n",
			want:  map[string]string{},
		},
		{
			name:  "values are whitespace-trimmed",
			input: "  sk  \n\n",
			want:  map[string]string{"CURSOR_API_KEY": "sk"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var rs secretReader
			if tc.secrets != nil {
				i := 0
				rs = func() (string, error) {
					v := tc.secrets[i]
					i++
					return v, nil
				}
			}
			var out strings.Builder
			got := promptEnv("cursor", vars, strings.NewReader(tc.input), &out, rs)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("promptEnv(input=%q, secrets=%v) mismatch (-want +got):\n%s", tc.input, tc.secrets, diff)
			}
			if !strings.Contains(out.String(), "CURSOR_API_KEY") {
				t.Errorf("prompt output should mention the var name, got:\n%s", out.String())
			}
		})
	}

	t.Run("secret reader error skips the var and keeps going", func(t *testing.T) {
		t.Parallel()
		rs := func() (string, error) { return "", errors.New("no tty") }
		var out strings.Builder
		got := promptEnv("cursor", vars, strings.NewReader("acme\n"), &out, rs)
		want := map[string]string{"CURSOR_TEAM": "acme"}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("promptEnv with failing secret reader mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestPromptLine(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	if got := promptLine("git user.name:", strings.NewReader("Ada\n"), &out); got != "Ada" {
		t.Errorf("promptLine = %q, want Ada", got)
	}
	if !strings.Contains(out.String(), "git user.name:") {
		t.Errorf("prompt must echo the label, got %q", out.String())
	}
}

func TestWizardEnabled(t *testing.T) {
	tests := []struct {
		val  string
		want bool
	}{
		{"", true}, {"on", true}, {"1", true},
		{"off", false}, {"0", false}, {"no", false}, {"false", false}, {"disabled", false},
	}
	for _, tc := range tests {
		t.Run("PROVEO_WIZARD="+tc.val, func(t *testing.T) {
			t.Setenv("PROVEO_WIZARD", tc.val)
			if got := run.WizardEnabled(); got != tc.want {
				t.Errorf("wizardEnabled() with PROVEO_WIZARD=%q = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

func TestNumberedPickersUseTheStatusVocabulary(t *testing.T) {
	var out strings.Builder
	got, err := pickTargetsNumbered([]maintain.Target{{Name: "hermes"}, {Name: "cecli"}}, "build", strings.NewReader("1\n"), &out)
	if err != nil || len(got) != 1 || got[0].Name != "hermes" {
		t.Fatalf("pick = %v, %v", got, err)
	}
	for _, want := range []string{"build", "select a target to build:", "   0) all", "   1) hermes", "target [0]: "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("target picker missing %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if p := pickProjectNumbered([]workspace.Project{{Path: "apps/web"}}, strings.NewReader("1\n"), &out); p != "apps/web" {
		t.Fatalf("scope = %q", p)
	}
	for _, want := range []string{"scope", "monorepo detected — choose a scope:", "   0) <repo root>", "   1) apps/web", "scope [0]: "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("scope picker missing %q:\n%s", want, out.String())
		}
	}
}

func TestAskfWaitsOnTheSameLine(t *testing.T) {
	var out strings.Builder
	if got := promptYesNo("build hermes?", true, strings.NewReader("\n"), &out); !got {
		t.Error("empty answer takes the default")
	}
	if s := out.String(); s != "build hermes? [Y/n] " {
		t.Errorf("plain prompt = %q, want the question and a trailing space, no newline", s)
	}
}
