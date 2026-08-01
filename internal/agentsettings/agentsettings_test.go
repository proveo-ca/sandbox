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

// The decision this package exists to enforce: a manifest change invalidates the
// cached answer rather than silently keeping a now-invalid cell.
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
