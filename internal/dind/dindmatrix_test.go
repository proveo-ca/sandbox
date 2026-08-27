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

	wantCapable := map[string]bool{"cecli": true, "opencode": true, "cursor": false, "claudecode": false}
	for _, m := range ms {
		want, tracked := wantCapable[m.Name]
		if !tracked {
			continue
		}
		if m.IsDind() != want {
			t.Errorf("%s: manifest docker=%q (dind=%v), want dind=%v", m.Name, m.Docker, m.IsDind(), want)
		}
		for _, intercepting := range []string{"allowlist", "review"} {
			if m.IsDind() && ModeSupported(intercepting) {
				t.Errorf("%s: DinD must never be offered under %s egress", m.Name, intercepting)
			}
		}
		// docker: sbx harnesses (cursor, claudecode) are not dind-capable at all —
		// the enum makes "both" unrepresentable, so there is nothing to subtract.
		wantSidecar := want
		got := m.IsDind() && ModeSupported("open") && ShouldStart(m.IsDind(), scope, false, nil)
		if got != wantSidecar {
			t.Errorf("%s: open-tier sidecar start = %v, want %v", m.Name, got, wantSidecar)
		}
	}
}
