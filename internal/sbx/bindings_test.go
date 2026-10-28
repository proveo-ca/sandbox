package sbx

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// SPEC: _spec/internal/sbx/credential-resolution.puml (R9)
func TestLoadBindingsReadsWhichMechanismEachServiceApproves(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "credentials.yaml")
	if err := os.WriteFile(p, []byte(`bindings:
  anthropic:
    apiKey:
      domains: [api.anthropic.com]
    oauth:
      domains: [platform.claude.com]
  openai:
    apiKey:
      domains: [openai.com]
  cursor:
    oauth:
      domains: [api2.cursor.sh]
remembered:
  /w:
    github: github@work
`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadBindings(p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Binding{
		"anthropic": {APIKey: true, OAuth: true},
		"openai":    {APIKey: true},
		"cursor":    {OAuth: true},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("LoadBindings mismatch (-want +got):\n%s", diff)
	}
}

func TestAMissingBindingsFileApprovesNothing(t *testing.T) {
	t.Parallel()
	got, err := LoadBindings(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil || len(got) != 0 {
		t.Errorf("LoadBindings(absent) = %v, %v; want empty, nil", got, err)
	}
}

// SPEC: _spec/internal/sbx/credential-resolution.puml (R9)
func TestStoreEntriesSplitKeysFromOAuthAndSkipScopedRows(t *testing.T) {
	t.Parallel()
	got := parseStoreEntries(`SCOPE      TYPE      NAME                SECRET
(global)   service   OPENAI_API_KEY      (stored)
(global)   service   cursor              (stored)
(global)   service   anthropic           (oauth configured)
(global)   service   openai              (oauth configured)
p0-m5      service   groq                (stored)

CUSTOM SECRETS
SCOPE      TARGETS          ENV        PLACEHOLDER        SECRET
(global)   api.example.com  EXAMPLE    sbx-cs-abc         (stored)
`)
	want := map[string]StoreEntry{
		"OPENAI_API_KEY": {APIKey: true},
		"cursor":         {APIKey: true},
		"anthropic":      {OAuth: true},
		"openai":         {OAuth: true},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("parseStoreEntries mismatch (-want +got):\n%s", diff)
	}
}

// SPEC: _spec/internal/sbx/credential-resolution.puml (R5, R9)
func TestMechanismGatesOnlyTheAPIKeyOnItsApproval(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		e        StoreEntry
		b        Binding
		use      string
		withheld []string
	}{
		{"key approved", StoreEntry{APIKey: true}, Binding{APIKey: true}, "apikey", nil},
		{"key unapproved", StoreEntry{APIKey: true}, Binding{}, "", []string{"apikey"}},
		{"key outranks oauth", StoreEntry{APIKey: true, OAuth: true}, Binding{APIKey: true}, "apikey", nil},
		{"unapproved key falls back to oauth", StoreEntry{APIKey: true, OAuth: true}, Binding{}, "oauth", []string{"apikey"}},
		{"oauth needs no approval", StoreEntry{OAuth: true}, Binding{APIKey: true}, "oauth", nil},
		{"nothing stored", StoreEntry{}, Binding{APIKey: true, OAuth: true}, "", nil},
	} {
		use, withheld := Mechanism(tc.e, tc.b)
		if use != tc.use || !cmp.Equal(withheld, tc.withheld) {
			t.Errorf("%s: Mechanism = %q, %v; want %q, %v", tc.name, use, withheld, tc.use, tc.withheld)
		}
	}
}
