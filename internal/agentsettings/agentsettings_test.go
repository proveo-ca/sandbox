package agentsettings

import (
	"os"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
)

func TestMissingFileIsFirstRunNotAnError(t *testing.T) {
	t.Parallel()
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load on a fresh root: %v", err)
	}
	if _, ok := s.Lookup("opencode", manifest.Capabilities{}); ok {
		t.Error("an empty store must not report a cached choice")
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	caps := manifest.Capabilities{Egress: []string{"open"}, Credentials: []string{"forward"}}

	s, _ := Load(root)
	s.Remember("cursor", caps, Choice{Egress: "open", Credentials: "forward", Addons: []string{"browser"}})
	if err := s.Save(root); err != nil {
		t.Fatal(err)
	}

	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	ch, ok := got.Lookup("cursor", caps)
	if !ok {
		t.Fatal("a stored choice must load back")
	}
	if ch.Egress != "open" || ch.Credentials != "forward" || len(ch.Addons) != 1 || ch.Addons[0] != "browser" {
		t.Errorf("round-trip mismatch: %+v", ch)
	}
}

func TestCachedChoiceDoesNotSurviveACapabilityChange(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	before := manifest.Capabilities{Egress: []string{"open", "allowlist"}}
	after := manifest.Capabilities{Egress: []string{"open"}}

	s, _ := Load(root)
	s.Remember("opencode", before, Choice{Egress: "allowlist", Credentials: "broker"})
	if err := s.Save(root); err != nil {
		t.Fatal(err)
	}

	got, _ := Load(root)
	if _, ok := got.Lookup("opencode", before); !ok {
		t.Error("unchanged capabilities must still hit")
	}
	if _, ok := got.Lookup("opencode", after); ok {
		t.Error("changed capabilities must MISS so the operator is re-prompted")
	}
}

func TestFingerprintIsOrderAndCaseStable(t *testing.T) {
	t.Parallel()
	a := manifest.Capabilities{Egress: []string{"open", "allowlist"}, Providers: []string{"anthropic"}}
	b := manifest.Capabilities{Egress: []string{"ALLOWLIST", " open "}, Providers: []string{"Anthropic"}}
	if Fingerprint(a) != Fingerprint(b) {
		t.Error("fingerprint must ignore ordering, case and padding")
	}
	c := manifest.Capabilities{Egress: []string{"open"}, Providers: []string{"anthropic"}}
	if Fingerprint(a) == Fingerprint(c) {
		t.Error("dropping a cell must change the fingerprint")
	}
}

func TestSaveIsOwnerOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s, _ := Load(root)
	s.Remember("cecli", manifest.Capabilities{}, Choice{Egress: "allowlist", Credentials: "broker"})
	if err := s.Save(root); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("agent-settings.yml perm = %04o, want 0600", perm)
	}
}

// TestModelsSurviveSaveAndLoad covers the round trip the run path relies on: a
// session that set three roles must come back with them, so an operator does not
// retype ARCHITECT_MODEL/EDITOR_MODEL/SMALL_MODEL on every run.
func TestModelsSurviveSaveAndLoad(t *testing.T) {
	root := t.TempDir()
	caps := manifest.Capabilities{Egress: []string{"allowlist"}, Credentials: []string{"broker"}}

	s, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Remember("opencode", caps, Choice{
		Egress: "allowlist", Credentials: "broker",
		Models: map[string]string{"main": "kimi-k3", "editor": "grok-4-5", "small": "grok-4-5-fast"},
	})
	if err := s.Save(root); err != nil {
		t.Fatal(err)
	}

	again, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := again.Lookup("opencode", caps)
	if !ok {
		t.Fatal("entry did not survive the round trip")
	}
	for role, want := range map[string]string{"main": "kimi-k3", "editor": "grok-4-5", "small": "grok-4-5-fast"} {
		if got.Models[role] != want {
			t.Errorf("models[%s] = %q, want %q", role, got.Models[role], want)
		}
	}
}

// Models are the operator's choice, not a capability, so a manifest change must
// discard the axes (which may no longer be valid) without being able to take the
// model assignment with it — the entry is dropped as a whole, and the next run
// re-reads the roles from the environment rather than inheriting a stale set.
func TestModelsAreNotPartOfTheFingerprint(t *testing.T) {
	caps := manifest.Capabilities{Egress: []string{"allowlist"}}
	a := Fingerprint(caps)
	b := Fingerprint(caps) // same capabilities, regardless of any model
	if a != b {
		t.Fatalf("fingerprint is not stable for identical capabilities: %q vs %q", a, b)
	}
	wider := Fingerprint(manifest.Capabilities{Egress: []string{"allowlist", "review"}})
	if a == wider {
		t.Error("a capability change must change the fingerprint")
	}
}
