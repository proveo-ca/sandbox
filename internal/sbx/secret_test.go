package sbx

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// SPEC: _spec/internal/sbx/credential-resolution.puml (R6)
func TestAPIKeyServicesAreTheStoredRowsOnly(t *testing.T) {
	restore := sh
	t.Cleanup(func() { sh = restore })
	sh.SecretList = func() ([]byte, error) {
		return []byte(`SCOPE      TYPE      NAME                      SECRET
(global)   service   CURSOR_API_KEY            (stored)
(global)   service   cursor                    (stored)
(global)   service   github                    (stored)
(global)   service   anthropic                 (oauth configured)
(global)   service   openai                    (oauth configured)

CUSTOM SECRETS
SCOPE      TARGETS                       ENV                    PLACEHOLDER               SECRET
(global)   api.example.com               EXAMPLE_KEY            sbx-cs-abc                (stored)
`), nil
	}
	got := APIKeyServices()
	want := []string{"CURSOR_API_KEY", "cursor", "github"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("APIKeyServices() mismatch (-want +got):\n%s", diff)
	}
}
