// SPEC: _spec/_plans/opencode-versioned-history-storage.puml
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
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

func TestOpenCodeHistorySeesOnlyARealV2Database(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "opencode/share"), 0o700); err != nil {
		t.Fatal(err)
	}
	v2, legacy := openCodeHistory(home)
	if v2 || len(legacy) != 1 || legacy[0] != "opencode/share" {
		t.Fatalf("history = %v %v", v2, legacy)
	}
	db := filepath.Join(home, "opencode/v2/share")
	if err := os.MkdirAll(db, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(db, "opencode.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	v2, legacy = openCodeHistory(home)
	if !v2 || len(legacy) != 1 {
		t.Fatalf("database not selected: %v %v", v2, legacy)
	}
}

func TestUnpreparedLegacyStoreAsksAndKeepsTheSandbox(t *testing.T) {
	previous := receiptDir
	root := t.TempDir()
	receiptDir = func() string { return root }
	t.Cleanup(func() { receiptDir = previous })
	t.Setenv("PROVEO_WIZARD", "off")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "opencode/share"), 0o700); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	run := func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "--bootstrap-cache" {
			return `{"cache_owner":"1234:5678"}`, nil
		}
		return "proveo: OpenCode history is not prepared", fmt.Errorf("exit")
	}
	_, err := prepareOpenCodeLaunch(Input{Target: "opencode", HomeRoot: home}, sbx.RunConfig{
		Name: "proveo-opencode-11c4aed0", Command: []string{"--proveo-prepare"},
	}, func(string) bool { return true }, run)
	if err == nil || !strings.Contains(err.Error(), "asks again") || !strings.Contains(err.Error(), "opencode/share") {
		t.Fatalf("err = %v", err)
	}
	for _, call := range calls {
		if call[0] == "--prepare" {
			t.Fatalf("declined prepare still ran: %v", calls)
		}
	}
}

func TestLegacyDatabaseIsCopiedAndPrepared(t *testing.T) {
	previous := receiptDir
	root := t.TempDir()
	receiptDir = func() string { return root }
	t.Cleanup(func() { receiptDir = previous })
	t.Setenv("PROVEO_WIZARD", "off")
	home := t.TempDir()
	legacy := filepath.Join(home, "opencode", "share")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("legacy-database")
	if err := os.WriteFile(filepath.Join(legacy, "opencode.db"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	asked := false
	previousConfirm := confirmEmptyPrepare
	confirmEmptyPrepare = func([]string) bool {
		asked = true
		return false
	}
	t.Cleanup(func() { confirmEmptyPrepare = previousConfirm })
	var calls [][]string
	run := func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "--bootstrap-cache", "--prepare":
			return "warning: ignored\n{\"cache_owner\":\"1234:5678\"}\n", nil
		default:
			return "proveo: OpenCode history is not prepared", fmt.Errorf("exit")
		}
	}
	_, err := prepareOpenCodeLaunch(Input{Target: "opencode", HomeRoot: home}, sbx.RunConfig{
		Name: "proveo-opencode-11c4aed0",
	}, func(string) bool { return true }, run)
	copied, readErr := os.ReadFile(filepath.Join(home, "opencode", "v2", "share", "opencode.db"))
	if readErr != nil || string(copied) != string(payload) {
		t.Fatalf("copied = %q err=%v", copied, readErr)
	}
	kept, _ := os.ReadFile(filepath.Join(legacy, "opencode.db"))
	if string(kept) != string(payload) {
		t.Fatal("legacy database was modified")
	}
	if asked {
		t.Fatal("an existing database asked for an empty prepare")
	}
	var prepare []string
	for _, call := range calls {
		if call[0] == "--prepare" {
			prepare = call
		}
		if slices.Contains(call, "--empty") {
			t.Fatalf("adopted database was prepared empty: %v", calls)
		}
	}
	if prepare == nil {
		t.Fatalf("legacy database was not prepared: %v err=%v", calls, err)
	}
}

func TestUnpreparedV2DatabasePreparesOnOrdinaryLaunch(t *testing.T) {
	previous := receiptDir
	root := t.TempDir()
	receiptDir = func() string { return root }
	t.Cleanup(func() { receiptDir = previous })
	t.Setenv("PROVEO_WIZARD", "off")
	home := t.TempDir()
	db := filepath.Join(home, "opencode", "v2", "share")
	if err := os.MkdirAll(db, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(db, "opencode.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	run := func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "--bootstrap-cache", "--prepare":
			return `{"cache_owner":"1234:5678"}`, nil
		default:
			return "proveo: OpenCode history is not prepared", fmt.Errorf("exit")
		}
	}
	_, err := prepareOpenCodeLaunch(Input{Target: "opencode", HomeRoot: home}, sbx.RunConfig{
		Name: "proveo-opencode-11c4aed0",
	}, func(string) bool { return true }, run)
	var prepare []string
	for _, call := range calls {
		if call[0] == "--prepare" {
			prepare = call
		}
		if slices.Contains(call, "--empty") {
			t.Fatalf("existing V2 database was prepared empty: %v", calls)
		}
	}
	if prepare == nil || !slices.Contains(prepare, "--proveo-cache-owner=1234:5678") {
		t.Fatalf("ordinary launch did not prepare the mounted V2 database: %v", calls)
	}
	if err != nil && strings.Contains(err.Error(), "explicitly mounted") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareFailureKeepsTheRuntimeText(t *testing.T) {
	previous := receiptDir
	root := t.TempDir()
	receiptDir = func() string { return root }
	t.Cleanup(func() { receiptDir = previous })
	t.Setenv("PROVEO_WIZARD", "off")
	home := t.TempDir()
	db := filepath.Join(home, "opencode", "v2", "share")
	if err := os.MkdirAll(db, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(db, "opencode.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[0] {
		case "--bootstrap-cache":
			return `{"cache_owner":"1234:5678"}`, nil
		case "--prepare":
			return "proveo: history has pending private state; recover it explicitly", exec.Command("sh", "-c", "exit 75").Run()
		default:
			return "proveo: OpenCode history is not prepared", fmt.Errorf("exit")
		}
	}
	_, err := prepareOpenCodeLaunch(Input{Target: "opencode", HomeRoot: home}, sbx.RunConfig{
		Name: "proveo-opencode-11c4aed0",
	}, func(string) bool { return true }, run)
	if err == nil || !strings.Contains(err.Error(), "pending private state") {
		t.Fatalf("err = %v", err)
	}
	var agent backend.ExitError
	if errors.As(err, &agent) {
		t.Fatal("prepare failure was reported as an agent exit")
	}
}

func TestInvalidOwnerExplainsThatTheSessionDoesNotPersist(t *testing.T) {
	previous := receiptDir
	root := t.TempDir()
	receiptDir = func() string { return root }
	t.Cleanup(func() { receiptDir = previous })
	t.Setenv("PROVEO_WIZARD", "off")
	_, err := prepareOpenCodeLaunch(Input{Target: "opencode", HomeRoot: t.TempDir()}, sbx.RunConfig{Name: "proveo-opencode-11c4aed0"},
		func(string) bool { return true }, func(context.Context, string, ...string) (string, error) {
			return `{"cache_owner":"fake"}`, nil
		})
	if err == nil || !strings.Contains(err.Error(), "invalid owner identity") || !strings.Contains(err.Error(), "does not persist") || !strings.Contains(err.Error(), "reattaches") {
		t.Fatalf("err = %v", err)
	}
}

func TestBootstrapFailureAfterRemovalDoesNotSayTheSandboxStays(t *testing.T) {
	t.Parallel()
	err := openCodeBootstrapFailure(fmt.Errorf("OpenCode cache bootstrap returned invalid owner identity"), "proveo-opencode-11c4aed0", true)
	if strings.Contains(err.Error(), "sandbox stays") || !strings.Contains(err.Error(), "was removed") || !strings.Contains(err.Error(), "does not persist") {
		t.Fatalf("err = %v", err)
	}
}

func TestRecoverIsNotRewrittenAsPrepare(t *testing.T) {
	previous := receiptDir
	root := t.TempDir()
	receiptDir = func() string { return root }
	t.Cleanup(func() { receiptDir = previous })
	t.Setenv("PROVEO_WIZARD", "off")
	var calls [][]string
	run := func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "--bootstrap-cache", "--recover":
			return `{"cache_owner":"1234:5678"}`, nil
		default:
			return "", fmt.Errorf("unexpected %s", args[0])
		}
	}
	_, err := prepareOpenCodeLaunch(Input{Target: "opencode", HomeRoot: t.TempDir()}, sbx.RunConfig{
		Name: "proveo-opencode-11c4aed0", Command: []string{"--proveo-recover"},
	}, func(string) bool { return true }, run)
	sawRecover := false
	for _, call := range calls {
		if call[0] == "--prepare" || call[0] == "--check-prepared" {
			t.Fatalf("recover took the prepare path: %v", calls)
		}
		if call[0] == "--recover" {
			sawRecover = true
		}
	}
	if !sawRecover {
		t.Fatalf("recover did not run: %v err=%v", calls, err)
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
