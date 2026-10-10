// SPEC: _spec/_plans/opencode-versioned-history-storage.puml
package sandbox

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
)

func TestOpenCodeCacheOwnerLivesOutsideGuestState(t *testing.T) {
	previous := receiptDir
	root := t.TempDir()
	receiptDir = func() string { return root }
	t.Cleanup(func() { receiptDir = previous })
	var calls [][]string
	run := func(name string, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		return `{"cache_owner":"1234:5678"}`, nil
	}
	for range 2 {
		owner, err := openCodeCacheOwner("fixture", run)
		if err != nil || owner != "1234:5678" {
			t.Fatalf("owner=%q err=%v", owner, err)
		}
	}
	if !reflect.DeepEqual(calls[0], []string{"--bootstrap-cache"}) || !reflect.DeepEqual(calls[1], []string{"--bootstrap-cache", "--proveo-cache-owner=1234:5678"}) {
		t.Fatalf("host did not preserve its own owner identity: %v", calls)
	}
	info, err := os.Stat(cacheOwnerPath("fixture"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("owner record permission: %v %v", info, err)
	}
	_, err = openCodeCacheOwner("bad", func(string, ...string) (string, error) { return `{"cache_owner":"fake"}`, nil })
	if err == nil {
		t.Fatal("accepted a malformed cache owner identity")
	}
}

func TestOpenCodePreparationMountsOnlyExplicitReadOnlySource(t *testing.T) {
	t.Parallel()
	source := t.TempDir()
	mount, enabled, err := openCodePreparationSource([]string{"--proveo-prepare", "--source", source})
	if err != nil || !enabled || mount.Host != source || !mount.ReadOnly {
		t.Fatalf("preparation source: %+v %v %v", mount, enabled, err)
	}
	_, enabled, err = openCodePreparationSource([]string{"run", "--source", source})
	if err != nil || enabled {
		t.Fatalf("ordinary launch mounted a preparation source: %v %v", enabled, err)
	}
	link := filepath.Join(t.TempDir(), "source")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	_, _, err = openCodePreparationSource([]string{"--proveo-prepare", "--source", link})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("accepted source symlink: %v", err)
	}
}

func TestOpenCodeV2DoesNotMountUnversionedHistory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	other := filepath.Join(root, "opencode/v1/share")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	home := manifest.Home{Enabled: true, Mounts: []manifest.HomeMount{{Host: "opencode/v2/share", Container: "/proveo-home/.local/share/opencode"}}}
	access, err := PrepareHomeAccess(root, t.TempDir(), home)
	if err != nil {
		t.Fatal(err)
	}
	for _, mount := range access.Mounts {
		if mount.Host == other {
			t.Fatal("V1/unversioned history entered V2 mounts")
		}
	}
}
