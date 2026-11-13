// SPEC: _spec/cmd/proveo/github-access.puml
package run

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

func TestCredentialInventoryDeduplicatesMetadataWithoutRenderingSecretValues(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{Name: "claudecode", Env: []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}}}
	values := map[string]string{"OPENAI_API_KEY": "test-api-secret", "CLAUDE_CODE_OAUTH_TOKEN": "test-oauth-secret"}
	store := map[string]sbx.StoreEntry{"openai": {APIKey: true, OAuth: true}, "anthropic": {OAuth: true}, "github": {APIKey: true}}
	ssh := []string{"SHA256:public-fingerprint (ED25519)"}
	got := inventoryOf(man, func(name string) string { return values[name] }, store, ssh)
	if !slices.Equal(got.APIKeys, []string{"github", "openai"}) || !slices.Equal(got.OAuth, []string{"anthropic", "openai"}) || !slices.Equal(got.SSH, ssh) {
		t.Fatalf("inventory = %#v", got)
	}
	row := credentialsRow(got)
	for _, secret := range values {
		if strings.Contains(rowText(row), secret) {
			t.Fatal("credential inventory rendered a secret value")
		}
	}
	ssh[0] = "changed"
	if got.SSH[0] != "SHA256:public-fingerprint (ED25519)" {
		t.Fatal("inventory shares mutable SSH input")
	}
}

// rowText is the row and every list it opens, as Go syntax.
func rowText(row choiceui.Row) string {
	text := fmt.Sprintf("%#v", row)
	for _, m := range row.Lists {
		text += fmt.Sprintf(" %#v", *m)
	}
	return text
}

func TestTheEnvCellListsTheEnvFilesNamesAndNeverAValue(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ".env")
	values := []string{"envvalue-zz-0117", "envvalue-qq-4471", "envvalue-ww-9023"}
	body := "zeta=" + values[0] + "\nexport ALPHA=\"" + values[1] + "\"\n=" + values[2] + "\nbeta='x'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	row := credentialsRow(credentialInventory{}.withEnvFile(path))
	env := row.Lists[3]
	if env == nil || !slices.Equal(env.Options, []string{"ALPHA", "beta", "zeta"}) || !row.On[3] {
		t.Fatalf(".env cell = %#v", row)
	}
	for _, v := range values {
		if strings.Contains(rowText(row), v) {
			t.Fatalf("the .env value %q reached the row: %s", v, rowText(row))
		}
	}
	if !strings.Contains(env.Help["zeta"], ui.Tilde(path)) {
		t.Errorf(".env help %q does not name the file", env.Help["zeta"])
	}
}

func TestEmptyCredentialClassesStayUntickedWithoutDropdowns(t *testing.T) {
	t.Parallel()
	row := credentialsRow(credentialInventory{OAuth: []string{"openai"}})
	if !slices.Equal(row.On, []bool{false, true, false, false}) || row.Lists[0] != nil || row.Lists[2] != nil || row.Lists[1] == nil || row.Lists[3] != nil {
		t.Fatalf("empty classes advertised credentials: %#v", row)
	}
	missing := credentialsRow(credentialInventory{}.withEnvFile(filepath.Join(t.TempDir(), "absent.env")))
	if missing.On[3] || missing.Lists[3] != nil {
		t.Fatalf("an env file with no names advertised a list: %#v", missing)
	}
}
