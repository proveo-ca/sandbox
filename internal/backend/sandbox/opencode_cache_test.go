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
	"github.com/proveo-ca/proveo/internal/choiceui"
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

func init() {
	seedOpenCode = func(context.Context, string, string) ([]byte, error) { return nil, nil }
	removeOpenCodeEngine = func(name string) error {
		return fmt.Errorf("test removed %s without stubbing removeOpenCodeEngine", name)
	}
	createOpenCodeEngine = func(cfg sbx.RunConfig) error {
		return fmt.Errorf("test created %s without stubbing createOpenCodeEngine", cfg.Name)
	}
}

const lostKeeperEngine = "proveo-opencode-test-0000"

// lostKeeperHome is a proveo home whose V2 store holds a database and, unless phase is empty, a journal engine wrote at phase.
func lostKeeperHome(t *testing.T, engine, phase string) string {
	t.Helper()
	previous := receiptDir
	root := t.TempDir()
	receiptDir = func() string { return root }
	t.Cleanup(func() { receiptDir = previous })
	home := t.TempDir()
	share := filepath.Join(home, "opencode", "v2", "share")
	if err := os.MkdirAll(share, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(share, "opencode.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if phase != "" {
		record := `{"format":1,"engine":"` + engine + `","phase":"` + phase + `"}`
		if err := os.WriteFile(filepath.Join(share, openCodeJournalName), []byte(record), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// lostKeeperRuntime answers like a fresh keeper facing engine-local history it never attested.
// prepare succeeds once removed reports true; recoverOut and recoverErr are what --recover returns.
func lostKeeperRuntime(calls *[][]string, removed *bool, recoverOut string, recoverErr error) func(context.Context, string, ...string) (string, error) {
	return func(_ context.Context, _ string, args ...string) (string, error) {
		*calls = append(*calls, append([]string(nil), args...))
		switch args[0] {
		case "--bootstrap-cache":
			return `{"cache_owner":"1234:5678"}`, nil
		case "--prepare":
			if removed != nil && *removed {
				return `{"cache_owner":"1234:5678"}`, nil
			}
			return "proveo: unattested retained state requires explicit recovery, not replacement", exec.Command("sh", "-c", "exit 78").Run()
		case "--recover":
			if recoverErr != nil {
				return recoverOut, recoverErr
			}
			return `{"cache_owner":"1234:5678"}`, nil
		default:
			return "proveo: OpenCode history is not prepared; run --proveo-prepare or --proveo-recover", exec.Command("sh", "-c", "exit 78").Run()
		}
	}
}

func stubRetainedChoice(t *testing.T, answer choiceui.RetainedRecovery) *string {
	t.Helper()
	asked := ""
	previous := chooseRetainedRecovery
	chooseRetainedRecovery = func(_, phase string) choiceui.RetainedRecovery {
		asked = "asked:" + phase
		return answer
	}
	t.Cleanup(func() { chooseRetainedRecovery = previous })
	return &asked
}

func stubResetConfirm(t *testing.T, answer bool) *bool {
	t.Helper()
	asked := false
	previous := confirmSandboxReset
	confirmSandboxReset = func(string) bool { asked = true; return answer }
	t.Cleanup(func() { confirmSandboxReset = previous })
	return &asked
}

func stubEngineRemoval(t *testing.T) *bool {
	t.Helper()
	removed := false
	previous := removeOpenCodeEngine
	removeOpenCodeEngine = func(string) error { removed = true; return nil }
	t.Cleanup(func() { removeOpenCodeEngine = previous })
	return &removed
}

func operations(calls [][]string) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c[0])
	}
	return out
}

func lostKeeperLaunch(t *testing.T, home string, command []string, run func(context.Context, string, ...string) (string, error)) (sbx.RunConfig, error) {
	t.Helper()
	return prepareOpenCodeLaunch(Input{Target: "opencode", HomeRoot: home}, sbx.RunConfig{Name: lostKeeperEngine, Command: command},
		func(string) bool { return true }, run)
}

func TestLostKeeperAfterACleanStopRecoversWithoutAsking(t *testing.T) {
	for _, phase := range []string{"prepared", "published"} {
		t.Run(phase, func(t *testing.T) {
			home := lostKeeperHome(t, lostKeeperEngine, phase)
			asked := stubRetainedChoice(t, choiceui.RetainedStop)
			var calls [][]string
			cfg, err := lostKeeperLaunch(t, home, nil, lostKeeperRuntime(&calls, nil, "", nil))
			if err != nil {
				t.Fatalf("launch after a clean stop: %v", err)
			}
			if want := []string{"--bootstrap-cache", "--check-prepared", "--prepare", "--recover"}; !slices.Equal(operations(calls), want) {
				t.Fatalf("runtime calls = %v, want %v", operations(calls), want)
			}
			if !slices.Contains(calls[3], "--proveo-cache-owner=1234:5678") || !slices.Contains(cfg.Command, "--proveo-cache-owner=1234:5678") {
				t.Errorf("recover or the agent launch lost the keeper's owner: recover %v, agent %v", calls[3], cfg.Command)
			}
			if *asked != "" {
				t.Errorf("a clean stop prompted the operator (%s)", *asked)
			}
		})
	}
}

func TestInterruptedRunAsksThenRecovers(t *testing.T) {
	home := lostKeeperHome(t, lostKeeperEngine, "active")
	asked := stubRetainedChoice(t, choiceui.RetainedRecover)
	var calls [][]string
	if _, err := lostKeeperLaunch(t, home, nil, lostKeeperRuntime(&calls, nil, "", nil)); err != nil {
		t.Fatal(err)
	}
	if *asked != "asked:active" || !slices.Contains(operations(calls), "--recover") {
		t.Errorf("interrupted run: prompt %q, calls %v; want it asked with the journal phase, then recovered", *asked, operations(calls))
	}
}

func TestDeclinedRecoveryKeepsTheSandboxAndSaysHow(t *testing.T) {
	home := lostKeeperHome(t, lostKeeperEngine, "publishing")
	stubRetainedChoice(t, choiceui.RetainedStop)
	removed := stubEngineRemoval(t)
	var calls [][]string
	_, err := prepareOpenCodeLaunch(Input{Target: "opencode-browser", HomeRoot: home}, sbx.RunConfig{Name: lostKeeperEngine},
		func(string) bool { return true }, lostKeeperRuntime(&calls, nil, "", nil))
	if slices.Contains(operations(calls), "--recover") || *removed {
		t.Fatalf("stop still touched the history: %v removed=%v", operations(calls), *removed)
	}
	for _, want := range []string{"journal phase publishing", "This sandbox stays", "proveo run opencode-browser -- --proveo-recover", "sbx rm --force " + lostKeeperEngine} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}

func TestHeadlessRunWithNoJournalRecovers(t *testing.T) {
	t.Setenv("PROVEO_WIZARD", "off")
	home := lostKeeperHome(t, "", "")
	var calls [][]string
	if _, err := lostKeeperLaunch(t, home, nil, lostKeeperRuntime(&calls, nil, "", nil)); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(operations(calls), "--recover") {
		t.Errorf("a headless run with no journal did not recover: %v", operations(calls))
	}
}

func TestFailedRecoveryHeadlessKeepsTheSandboxAndNamesTheReset(t *testing.T) {
	t.Setenv("PROVEO_WIZARD", "off")
	home := lostKeeperHome(t, lostKeeperEngine, "published")
	removed := stubEngineRemoval(t)
	var calls [][]string
	_, err := lostKeeperLaunch(t, home, nil, lostKeeperRuntime(&calls, nil,
		"proveo: published history changed; do not overwrite it during cache recovery", exec.Command("sh", "-c", "exit 78").Run()))
	if *removed {
		t.Fatal("a headless run removed the engine")
	}
	for _, want := range []string{"recover failed", "published history changed", "This sandbox stays", "sbx rm --force " + lostKeeperEngine} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
	if err != nil && strings.Contains(err.Error(), "--proveo-recover") {
		t.Errorf("err = %v; it repeats the recovery that just failed", err)
	}
}

func TestBusyRecoveryNeverOffersTheReset(t *testing.T) {
	home := lostKeeperHome(t, lostKeeperEngine, "prepared")
	asked := stubResetConfirm(t, true)
	removed := stubEngineRemoval(t)
	var calls [][]string
	_, err := lostKeeperLaunch(t, home, nil, lostKeeperRuntime(&calls, nil,
		"proveo: another OpenCode run owns this history store", exec.Command("sh", "-c", "exit 75").Run()))
	if *asked || *removed {
		t.Fatalf("a busy store offered or ran a reset: asked=%v removed=%v", *asked, *removed)
	}
	if err == nil || !strings.Contains(err.Error(), "Run again") {
		t.Errorf("err = %v", err)
	}
}

func TestResetRecreatesTheEngineAndKeepsItsReceipt(t *testing.T) {
	home := lostKeeperHome(t, lostKeeperEngine, "building")
	stubRetainedChoice(t, choiceui.RetainedReset)
	removed := stubEngineRemoval(t)
	created := false
	previous := createOpenCodeEngine
	createOpenCodeEngine = func(sbx.RunConfig) error { created = true; return nil }
	t.Cleanup(func() { createOpenCodeEngine = previous })
	writeReceipt(lostKeeperEngine, launchReceipt{Image: "proveo/opencode:test"})
	var calls [][]string
	_, err := prepareOpenCodeLaunch(Input{Target: "opencode", HomeRoot: home}, sbx.RunConfig{Name: lostKeeperEngine},
		func(string) bool { return !*removed || created }, lostKeeperRuntime(&calls, removed, "", nil))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--bootstrap-cache", "--check-prepared", "--prepare", "--bootstrap-cache", "--check-prepared", "--prepare"}
	if !*removed || !created || !slices.Equal(operations(calls), want) {
		t.Fatalf("reset: removed=%v created=%v calls=%v, want %v", *removed, created, operations(calls), want)
	}
	if readReceipt(lostKeeperEngine) == nil {
		t.Error("the reset dropped the launch receipt, so the next run cannot tell the recreated engine's settings")
	}
}

func TestAnotherEnginesCleanJournalOffersOnlyTheReset(t *testing.T) {
	home := lostKeeperHome(t, "proveo-opencode-other-1111", "published")
	asked := stubRetainedChoice(t, choiceui.RetainedRecover)
	confirm := stubResetConfirm(t, false)
	removed := stubEngineRemoval(t)
	var calls [][]string
	_, err := lostKeeperLaunch(t, home, nil, lostKeeperRuntime(&calls, nil, "", nil))
	if slices.Contains(operations(calls), "--recover") || *removed || *asked != "" {
		t.Fatalf("a stale copy was recovered or removed without a yes: calls=%v removed=%v asked=%q", operations(calls), *removed, *asked)
	}
	if !*confirm {
		t.Error("a stale copy did not offer the reset")
	}
	for _, want := range []string{"stale", "proveo-opencode-other-1111", "sbx rm --force " + lostKeeperEngine} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}

func TestAnotherEnginesLiveJournalLeavesBothAlone(t *testing.T) {
	home := lostKeeperHome(t, "proveo-opencode-other-1111", "active")
	confirm := stubResetConfirm(t, true)
	removed := stubEngineRemoval(t)
	var calls [][]string
	_, err := lostKeeperLaunch(t, home, nil, lostKeeperRuntime(&calls, nil, "", nil))
	if slices.Contains(operations(calls), "--recover") || *removed || *confirm {
		t.Fatalf("another engine's live run led to recover or reset: calls=%v removed=%v asked=%v", operations(calls), *removed, *confirm)
	}
	if err == nil || !strings.Contains(err.Error(), "holds this proveo home's history") {
		t.Errorf("err = %v", err)
	}
}

func TestExplicitPrepareNeverTurnsIntoRecovery(t *testing.T) {
	home := lostKeeperHome(t, lostKeeperEngine, "published")
	stubRetainedChoice(t, choiceui.RetainedRecover)
	var calls [][]string
	_, err := lostKeeperLaunch(t, home, []string{"--proveo-prepare"}, lostKeeperRuntime(&calls, nil, "", nil))
	if slices.Contains(operations(calls), "--recover") {
		t.Fatalf("--proveo-prepare ran a recovery: %v", operations(calls))
	}
	if err == nil || !strings.Contains(err.Error(), "--proveo-recover") {
		t.Errorf("err = %v, want the recover command", err)
	}
}

func stubAgentTerminal(t *testing.T, present bool) {
	t.Helper()
	previous := agentTerminal
	agentTerminal = func() bool { return present }
	t.Cleanup(func() { agentTerminal = previous })
}

func stubSeed(t *testing.T) *bool {
	t.Helper()
	seeded := false
	previous := seedOpenCode
	seedOpenCode = func(context.Context, string, string) ([]byte, error) { seeded = true; return nil, nil }
	t.Cleanup(func() { seedOpenCode = previous })
	return &seeded
}

// maintenanceRuntime answers like a keeper whose engine is prepared when checked is true.
func maintenanceRuntime(calls *[][]string, checked bool) func(context.Context, string, ...string) (string, error) {
	return func(_ context.Context, _ string, args ...string) (string, error) {
		*calls = append(*calls, append([]string(nil), args...))
		switch args[0] {
		case "--bootstrap-cache", "--prepare", "--recover":
			return `{"cache_owner":"1234:5678"}`, nil
		case "--check-prepared":
			if checked {
				return "", nil
			}
		}
		return "proveo: OpenCode history is not prepared; run --proveo-prepare or --proveo-recover", exec.Command("sh", "-c", "exit 78").Run()
	}
}

func TestHeadlessMaintenanceStopsBeforeTheAgent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command []string
		checked bool
		want    string
	}{
		{"prepare", []string{"--proveo-prepare"}, false, "--prepare"},
		{"prepare on a prepared engine", []string{"--proveo-prepare"}, true, "--check-prepared"},
		{"recover", []string{"--proveo-recover"}, false, "--recover"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := lostKeeperHome(t, "", "")
			stubAgentTerminal(t, false)
			seeded := stubSeed(t)
			var calls [][]string
			_, err := lostKeeperLaunch(t, home, tc.command, maintenanceRuntime(&calls, tc.checked))
			if !errors.Is(err, errOpenCodeMaintenanceDone) {
				t.Fatalf("err = %v, want the headless maintenance stop", err)
			}
			if !slices.Contains(operations(calls), tc.want) {
				t.Errorf("calls = %v, want %s", operations(calls), tc.want)
			}
			if *seeded {
				t.Error("a headless maintenance run seeded an agent launch")
			}
		})
	}
}

func TestMaintenanceStartsTheAgentWithATerminalOrANativeCommand(t *testing.T) {
	for _, tc := range []struct {
		name     string
		command  []string
		terminal bool
		native   []string
	}{
		{"terminal prepare", []string{"--proveo-prepare"}, true, nil},
		{"terminal recover", []string{"--proveo-recover"}, true, nil},
		{"headless prepare with a native command", []string{"--proveo-prepare", "session", "list"}, false, []string{"session", "list"}},
		{"headless ordinary launch", nil, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := lostKeeperHome(t, "", "")
			stubAgentTerminal(t, tc.terminal)
			seeded := stubSeed(t)
			var calls [][]string
			cfg, err := lostKeeperLaunch(t, home, tc.command, maintenanceRuntime(&calls, false))
			if err != nil {
				t.Fatalf("err = %v, calls = %v", err, operations(calls))
			}
			if !*seeded {
				t.Error("the agent launch was not seeded")
			}
			var native []string
			for _, arg := range cfg.Command {
				if !strings.HasPrefix(arg, "--proveo-") {
					native = append(native, arg)
				}
			}
			if !slices.Equal(native, tc.native) || !slices.Contains(cfg.Command, "--proveo-cache-owner=1234:5678") {
				t.Errorf("command = %v, want the owner flag and native %v", cfg.Command, tc.native)
			}
		})
	}
}

func TestOpenCodeJournalTreatsTheShareAsUntrusted(t *testing.T) {
	home := lostKeeperHome(t, lostKeeperEngine, "published")
	if engine, phase := openCodeJournal(home); engine != lostKeeperEngine || phase != "published" {
		t.Errorf("journal = %q %q, want %q published", engine, phase, lostKeeperEngine)
	}
	if engine, phase := openCodeJournal(lostKeeperHome(t, "", "")); engine != "" || phase != "" {
		t.Errorf("no journal read as %q %q", engine, phase)
	}
	share := filepath.Join(home, "opencode", "v2", "share")
	journal := filepath.Join(share, openCodeJournalName)
	for name, plant := range map[string]func() error{
		"symlink": func() error {
			target := filepath.Join(t.TempDir(), "elsewhere")
			if err := os.WriteFile(target, []byte(`{"engine":"`+lostKeeperEngine+`","phase":"published"}`), 0o600); err != nil {
				return err
			}
			return os.Symlink(target, journal)
		},
		"oversized": func() error { return os.WriteFile(journal, make([]byte, 65<<10+1), 0o600) },
		"odd phase": func() error { return os.WriteFile(journal, []byte(`{"engine":"x","phase":"\u001b[2Jpwned"}`), 0o600) },
		"not json":  func() error { return os.WriteFile(journal, []byte("garbage"), 0o600) },
		"directory": func() error { return os.Mkdir(journal, 0o700) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.RemoveAll(journal); err != nil {
				t.Fatal(err)
			}
			if err := plant(); err != nil {
				t.Fatal(err)
			}
			if _, phase := openCodeJournal(home); phase != "" {
				t.Errorf("an untrusted %s journal yielded phase %q", name, phase)
			}
		})
	}
}

func TestHostAndRuntimeAgreeOnTheRetainedHistoryContract(t *testing.T) {
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", "packages", "lib", rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cache, creds, runtime := read("opencode_cache.py"), read("opencode-credentials.py"), read("proveo-opencode-runtime")
	if !strings.Contains(cache, retainedRefusal) {
		t.Errorf("opencode_cache.py no longer refuses with %q; the host would stop recovering a lost keeper", retainedRefusal)
	}
	if !strings.Contains(creds, `LIFECYCLE_LOCK_NAME = "`+strings.TrimSuffix(openCodeJournalName, ".journal")+`"`) ||
		!strings.Contains(cache, `+ ".journal"`) {
		t.Errorf("the runtime's journal is no longer %s beside its lifecycle lock", openCodeJournalName)
	}
	for _, phase := range []string{`self.record(self.durable_signature, "prepared")`, `self.record(baseline, "published", transaction)`} {
		if !strings.Contains(cache, phase) {
			t.Errorf("opencode_cache.py no longer records %s; the host recovers those phases without asking", phase)
		}
	}
	if !strings.Contains(cache, `engine=self.cfg["engine"]`) || !strings.Contains(runtime, `os.environ.get("SANDBOX_VM_ID")`) {
		t.Error("the journal no longer names its engine by sandbox name; the host compares it with the engine it reattaches to")
	}
}
