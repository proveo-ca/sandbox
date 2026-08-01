package dind

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
)

func TestFleetDinDMatrix(t *testing.T) {
	ms, err := manifest.Load(filepath.Join("..", "..", "defs"))
	if err != nil {
		t.Fatalf("load manifests: %v", err)
	}
	scope := t.TempDir()
	if err := os.WriteFile(filepath.Join(scope, "Dockerfile"), []byte("FROM alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVEO_DIND", "1")

	wantCapable := map[string]bool{"cecli": true, "opencode": true, "cursor": true, "claudecode": false}
	for _, m := range ms {
		want, tracked := wantCapable[m.Name]
		if !tracked {
			continue
		}
		if m.Dind != want {
			t.Errorf("%s: manifest dind=%v, want %v", m.Name, m.Dind, want)
		}
		if got := m.Dind && ModeSupported("firewall"); got {
			t.Errorf("%s: DinD must never be offered under firewall egress", m.Name)
		}
		got := m.Dind && ModeSupported("broker") && ShouldStart(m.Dind, scope, false, nil)
		if got != want {
			t.Errorf("%s: broker-mode ShouldStart = %v, want %v", m.Name, got, want)
		}
	}
}
