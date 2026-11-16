package sbx

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// OUTPUT HOLD: `sbx secret set` runs with its output kept, so nothing it prints lands under a card;
// a failure carries sbx's last line, and never the value it was given.
func TestSecretSetKeepsSbxsOutputOffTheTerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stub sbx is a shell script")
	}
	bin := t.TempDir()
	stub := "#!/bin/sh\ncat >/dev/null\necho 'stored'\necho 'error: the store is locked' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, Binary), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	err = sbxSecretSet("openai", "sk-secret")
	os.Stderr = stderr
	_ = w.Close()
	leaked, _ := io.ReadAll(r)
	if len(leaked) != 0 {
		t.Errorf("sbx printed past the hold: %q", leaked)
	}
	if err == nil || !strings.Contains(err.Error(), "the store is locked") || strings.Contains(err.Error(), "sk-secret") {
		t.Errorf("err = %v, want sbx's last line and no value", err)
	}
}
