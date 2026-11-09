// SPEC: _spec/cmd/proveo/github-access.puml
package run

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
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
		if strings.Contains(fmt.Sprintf("%#v %#v", row, row.Lists), secret) {
			t.Fatal("credential inventory rendered a secret value")
		}
	}
	ssh[0] = "changed"
	if got.SSH[0] != "SHA256:public-fingerprint (ED25519)" {
		t.Fatal("inventory shares mutable SSH input")
	}
}

func TestEmptyCredentialClassesStayUntickedWithoutDropdowns(t *testing.T) {
	t.Parallel()
	row := credentialsRow(credentialInventory{OAuth: []string{"openai"}})
	if !slices.Equal(row.On, []bool{false, true, false}) || row.Lists[0] != nil || row.Lists[2] != nil || row.Lists[1] == nil {
		t.Fatalf("empty classes advertised credentials: %#v", row)
	}
}
