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

// lostKeeperHome is a proveo home whose V2 store holds a database and, unless phase is empty, a journal at phase.
func lostKeeperHome(t *testing.T, phase string) string {
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
		if err := os.WriteFile(filepath.Join(share, openCodeJournalName), []byte(`{"format":1,"phase":"`+phase+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// lostKeeperRuntime answers like a fresh keeper facing engine-local history it never attested.
// prepare succeeds once removed reports true; recoverErr is what --recover returns.
func lostKeeperRuntime(calls *[][]string, removed *bool, recoverErr error) func(context.Context, string, ...string) (string, error) {
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
				return "proveo: unattested recovery has no matching durable baseline; preserve private state", recoverErr
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

func TestLostKeeperAfterACleanStopRecoversWithoutAsking(t *testing.T) {
	for _, phase := range []string{"prepared", "published"} {
		t.Run(phase, func(t *testing.T) {
			home := lostKeeperHome(t, phase)
			asked := stubRetainedChoice(t, choiceui.RetainedStop)
			var calls [][]string
			_, err := prepareOpenCodeLaunch(Input{Target: "opencode", HomeRoot: home}, sbx.RunConfig{Name: "proveo-opencode-11c4aed0"},
				func(string) bool { return true }, lostKeeperRuntime(&calls, nil, nil))
			if want := []string{"--bootstrap-cache", "--check-prepared", "--prepare", "--recover"}; !slices.Equal(operations(calls), want) {
				t.Fatalf("runtime calls = %v, want %v (err %v)", operations(calls), want, err)
			}
			if !slices.Contains(calls[3], "--proveo-cache-owner=1234:5678") {
				t.Errorf("recover ran without the keeper's owner: %v", calls[3])
			}
			if *asked != "" {
				t.Errorf("a clean sbx auto-stop prompted the operator (%s)", *asked)
			}
			if err != nil && (strings.Contains(err.Error(), "prepare failed") || strings.Contains(err.Error(), "recover failed")) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestInterruptedRunAsksThenRecovers(t *testing.T) {
	home := lostKeeperHome(t, "active")
	asked := stubRetainedChoice(t, choiceui.RetainedRecover)
	var calls [][]string
	_, _ = prepareOpenCodeLaunch(Input{Target: "opencode", HomeRoot: home}, sbx.RunConfig{Name: "proveo-opencode-11c4aed0"},
		func(string) bool { return true }, lostKeeperRuntime(&calls, nil, nil))
	if *asked != "asked:active" {
		t.Errorf("interrupted run: prompt = %q, want it asked with the journal phase", *asked)
	}
	if !slices.Contains(operations(calls), "--recover") {
		t.Errorf("recover answer did not recover: %v", operations(calls))
	}
}

func TestDeclinedRecoveryKeepsTheSandboxAndSaysHow(t *testing.T) {
	home := lostKeeperHome(t, "publishing")
	stubRetainedChoice(t, choiceui.RetainedStop)
	removed := stubEngineRemoval(t)
	var calls [][]string
	_, err := prepareOpenCodeLaunch(Input{Target: "opencode-browser", HomeRoot: home}, sbx.RunConfig{Name: "proveo-opencode-browser-11c4aed0"},
		func(string) bool { return true }, lostKeeperRuntime(&calls, nil, nil))
	if slices.Contains(operations(calls), "--recover") || *removed {
		t.Fatalf("stop still touched the history: %v removed=%v", operations(calls), *removed)
	}
	for _, want := range []string{"journal phase publishing", "This sandbox stays", "proveo run opencode-browser -- --proveo-recover", "sbx rm --force proveo-opencode-browser-11c4aed0"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}

func TestHeadlessInterruptedRunRecovers(t *testing.T) {
	t.Setenv("PROVEO_WIZARD", "off")
	home := lostKeeperHome(t, "")
	var calls [][]string
	_, _ = prepareOpenCodeLaunch(Input{Target: "opencode", HomeRoot: home}, sbx.RunConfig{Name: "proveo-opencode-11c4aed0"},
		func(string) bool { return true }, lostKeeperRuntime(&calls, nil, nil))
	if !slices.Contains(operations(calls), "--recover") {
		t.Errorf("a headless run with no journal did not recover: %v", operations(calls))
	}
}

func TestFailedRecoveryHeadlessNamesBothWaysOut(t *testing.T) {
	t.Setenv("PROVEO_WIZARD", "off")
	home := lostKeeperHome(t, "published")
	removed := stubEngineRemoval(t)
	var calls [][]string
	_, err := prepareOpenCodeLaunch(Input{Target: "opencode", HomeRoot: home}, sbx.RunConfig{Name: "proveo-opencode-11c4aed0"},
		func(string) bool { return true }, lostKeeperRuntime(&calls, nil, exec.Command("sh", "-c", "exit 75").Run()))
	if *removed {
		t.Fatal("a headless run removed the engine")
	}
	for _, want := range []string{"recover failed", "no matching durable baseline", "proveo run opencode -- --proveo-recover", "sbx rm --force proveo-opencode-11c4aed0"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}

func TestResetRemovesTheEngineThenPreparesAgain(t *testing.T) {
	home := lostKeeperHome(t, "building")
	stubRetainedChoice(t, choiceui.RetainedReset)
	removed := stubEngineRemoval(t)
	var calls [][]string
	_, _ = prepareOpenCodeLaunch(Input{Target: "opencode", HomeRoot: home}, sbx.RunConfig{Name: "proveo-opencode-11c4aed0"},
		func(string) bool { return true }, lostKeeperRuntime(&calls, removed, nil))
	want := []string{"--bootstrap-cache", "--check-prepared", "--prepare", "--bootstrap-cache", "--check-prepared", "--prepare"}
	if !*removed || !slices.Equal(operations(calls), want) {
		t.Fatalf("reset: removed=%v calls=%v, want %v", *removed, operations(calls), want)
	}
}

func TestExplicitPrepareNeverTurnsIntoRecovery(t *testing.T) {
	home := lostKeeperHome(t, "published")
	stubRetainedChoice(t, choiceui.RetainedRecover)
	var calls [][]string
	_, err := prepareOpenCodeLaunch(Input{Target: "opencode", HomeRoot: home}, sbx.RunConfig{Name: "proveo-opencode-11c4aed0", Command: []string{"--proveo-prepare"}},
		func(string) bool { return true }, lostKeeperRuntime(&calls, nil, nil))
	if slices.Contains(operations(calls), "--recover") {
		t.Fatalf("--proveo-prepare ran a recovery: %v", operations(calls))
	}
	if err == nil || !strings.Contains(err.Error(), "--proveo-recover") {
		t.Errorf("err = %v, want the recover command", err)
	}
}

func TestOpenCodeJournalPhaseReadsTheDurableJournal(t *testing.T) {
	if got := openCodeJournalPhase(lostKeeperHome(t, "published")); got != "published" {
		t.Errorf("phase = %q, want published", got)
	}
	if got := openCodeJournalPhase(lostKeeperHome(t, "")); got != "" {
		t.Errorf("phase with no journal = %q, want empty", got)
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
	cache, creds := read("opencode_cache.py"), read("opencode-credentials.py")
	if !strings.Contains(cache, retainedRefusal) {
		t.Errorf("opencode_cache.py no longer refuses with %q; the host would stop recovering a lost keeper", retainedRefusal)
	}
	if !strings.Contains(creds, `LIFECYCLE_LOCK_NAME = "`+strings.TrimSuffix(openCodeJournalName, ".journal")+`"`) ||
		!strings.Contains(cache, `+ ".journal"`) {
		t.Errorf("the runtime's journal is no longer %s beside its lifecycle lock", openCodeJournalName)
	}
	for _, clean := range []string{`self.record(self.durable_signature, "prepared")`, `self.record(baseline, "published", transaction)`} {
		if !strings.Contains(cache, clean) {
			t.Errorf("opencode_cache.py no longer records a clean phase as %s; the host reads it to skip the prompt", clean)
		}
	}
}
