package provider

import (
	"strings"
	"testing"
)

// The case from the field: roles spanning two vendors must attribute to both,
// which is what tells the operator whether a key is missing.
func TestRolesSpanningVendors(t *testing.T) {
	r := Roles{"ARCHITECT_MODEL": "kimi-3", "EDITOR_MODEL": "grok-4.5", "SMALL_MODEL": "grok-4.5-fast"}
	got := r.Providers()
	if len(got) != 2 {
		t.Fatalf("Providers() = %v, want moonshot and xai", got)
	}
	want := map[string]bool{"moonshot": true, "xai": true}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected provider %q", p)
		}
	}
}

func TestNormalizeIntent(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Kimi K3", "kimi-k3"},
		{"kimi_k3", "kimi-k3"},
		{"grok-4.5", "grok-4-5"},
		{"  GROK-4.5-Fast ", "grok-4-5-fast"},
		{"moonshot/kimi-k3", "moonshot/kimi-k3"}, // prefix preserved: it disambiguates
	} {
		if got := normalizeIntent(tc.in); got != tc.want {
			t.Errorf("normalizeIntent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMissingKeysNamesRoleAndVar(t *testing.T) {
	r := Roles{"ARCHITECT_MODEL": "kimi-k3", "EDITOR_MODEL": "grok-4.5"}
	msgs := r.MissingKeys([]string{"moonshot"}) // xai key absent
	if len(msgs) != 1 {
		t.Fatalf("want exactly one missing-key message, got %v", msgs)
	}
	for _, want := range []string{"EDITOR_MODEL", "grok-4.5", "XAI_API_KEY"} {
		if !strings.Contains(msgs[0], want) {
			t.Errorf("message %q should mention %q", msgs[0], want)
		}
	}
}

// An id no table attributes must not produce a warning: an unknown model is not
// evidence of a missing key, and a false alarm about a working model is worse.
func TestUnattributableModelIsSilent(t *testing.T) {
	r := Roles{"ARCHITECT_MODEL": "qwen-3-max"} // ambiguous open-weights family
	if got := r.Providers(); len(got) != 0 {
		t.Errorf("Providers() = %v, want none for an ambiguous id", got)
	}
	if got := r.MissingKeys(nil); len(got) != 0 {
		t.Errorf("MissingKeys() = %v, want silence", got)
	}
}

func TestCanonicalRoundTrip(t *testing.T) {
	r := Roles{"ARCHITECT_MODEL": "Kimi K3", "SMALL_MODEL": "grok-4.5-fast"}
	stored := r.Canonical()
	if stored["main"] != "kimi-k3" || stored["small"] != "grok-4-5-fast" {
		t.Fatalf("Canonical() = %v", stored)
	}
	back := RolesFromCanonical(stored)
	if back["ARCHITECT_MODEL"] != "kimi-k3" || back["SMALL_MODEL"] != "grok-4-5-fast" {
		t.Errorf("round trip lost data: %v", back)
	}
	if _, ok := back["EDITOR_MODEL"]; ok {
		t.Error("an unset role must stay unset")
	}
}
