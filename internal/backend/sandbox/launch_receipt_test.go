// SPEC: _spec/internal/sbx/kit-lifecycle.puml
package sandbox

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

func quietReport(string, ...any) {}

func stubReceiptDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	orig := receiptDir
	t.Cleanup(func() { receiptDir = orig })
	receiptDir = func() string { return dir }
}

func launchCfg(sid string, env ...string) sbx.RunConfig {
	return sbx.RunConfig{
		Name: "proveo-hermes-abc", Image: "proveo/hermes:local", Clone: true,
		Mounts: []sbx.Mount{{Host: "/w"}},
		Env:    append([]string{"SESSION_ID=" + sid, "FILES=/state/runs/" + sid + "/home"}, env...),
	}
}

func TestReceiptIgnoresThePerRunSessionID(t *testing.T) {
	t.Parallel()
	kit := func(sid string) []byte { return []byte("kitDir: /state/runs/" + sid + "/sbx/kit\n") }
	a := receiptOf(launchCfg("proveo-1-1"), launchCfg("proveo-1-1").Image, kit("proveo-1-1"), "proveo-1-1", "img1")
	b := receiptOf(launchCfg("proveo-2-2"), launchCfg("proveo-2-2").Image, kit("proveo-2-2"), "proveo-2-2", "img1")
	if got := b.changes(&a); got != nil {
		t.Errorf("two runs differing only by session ID must match, got %v", got)
	}
}

func TestReceiptNamesWhatChanged(t *testing.T) {
	t.Parallel()
	was := receiptOf(launchCfg("s1", "OLLAMA_API_BASE=http://host.docker.internal:11434"), launchCfg("s1", "OLLAMA_API_BASE=http://host.docker.internal:11434").Image, []byte("k1"), "s1", "img1")
	now := receiptOf(launchCfg("s2", "OLLAMA_API_BASE=http://host.docker.internal:11434", "PROVEO_HOST_CDP_PORT=9222"), launchCfg("s2", "OLLAMA_API_BASE=http://host.docker.internal:11434", "PROVEO_HOST_CDP_PORT=9222").Image,
		[]byte("k2"), "s2", "img2")

	got := now.changes(&was)
	for _, want := range []string{"image", "kit", "PROVEO_HOST_CDP_PORT"} {
		if !slices.Contains(got, want) {
			t.Errorf("changes = %v, missing %q", got, want)
		}
	}
	if slices.Contains(got, "OLLAMA_API_BASE") {
		t.Errorf("an unchanged variable was reported: %v", got)
	}
	if got := now.changes(nil); len(got) != 1 || !strings.Contains(got[0], "no record") {
		t.Errorf("a sandbox with no receipt must count as changed, got %v", got)
	}
}

func TestRetireIfStaleRecreatesOnlyAStoppedChangedSandbox(t *testing.T) {
	stubReceiptDir(t)
	cfg := launchCfg("s1")
	old := receiptOf(cfg, cfg.Image, []byte("k1"), "s1", "img")
	writeReceipt(cfg.Name, old)
	now := receiptOf(launchCfg("s2", "PROVEO_HOST_CDP_PORT=9222"), launchCfg("s2", "PROVEO_HOST_CDP_PORT=9222").Image, []byte("k2"), "s2", "img")

	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	retired := 0
	retire := func(Input, sbx.RunConfig, func(string, ...any)) error { retired++; return nil }
	quiet := func(string, ...any) {}
	quietWarn := func(string, ...string) {}

	if kept, err := retireIfStale(Input{}, cfg, staleChanges(cfg.Name, now, false), no, retire, quiet, quietWarn); err != nil || retired != 0 || kept != nil {
		t.Errorf("no sandbox: err=%v retired=%d kept=%q", err, retired, kept)
	}
	same := receiptOf(launchCfg("s3"), launchCfg("s3").Image, []byte("k1"), "s3", "img")
	if kept, err := retireIfStale(Input{}, cfg, staleChanges(cfg.Name, same, true), no, retire, quiet, quietWarn); err != nil || retired != 0 || kept != nil {
		t.Errorf("unchanged settings must re-attach: err=%v retired=%d kept=%q", err, retired, kept)
	}
	var warned string
	kept, err := retireIfStale(Input{}, cfg, staleChanges(cfg.Name, now, true), yes, retire, quiet,
		func(head string, detail ...string) { warned = head + "\n" + strings.Join(detail, "\n") })
	if err != nil || retired != 0 || !strings.Contains(warned, "not applied: ") || !strings.Contains(warned, "PROVEO_HOST_CDP_PORT") ||
		!slices.Contains(kept, "PROVEO_HOST_CDP_PORT") {
		t.Errorf("a RUNNING changed sandbox is someone's live session: warn, never remove: err=%v retired=%d warn=%q kept=%q", err, retired, warned, kept)
	}
	if head, _, _ := strings.Cut(warned, "\n"); strings.Contains(head, ". ") || strings.Count(head, " — ") != 1 {
		t.Errorf("the warning head is one sentence with one result, got %q", head)
	}
	var said string
	if kept, err := retireIfStale(Input{}, cfg, staleChanges(cfg.Name, now, true), no, retire, func(f string, a ...any) { said += fmt.Sprintf(f, a...) + "\n" }, quietWarn); err != nil || retired != 1 || kept != nil {
		t.Errorf("a stopped changed sandbox must be re-created: err=%v retired=%d kept=%q", err, retired, kept)
	}
	for _, want := range []string{"replacing " + cfg.Name, "PROVEO_HOST_CDP_PORT"} {
		if !strings.Contains(said, want) {
			t.Errorf("report = %q, missing %q", said, want)
		}
	}
}

func TestAHeadedYesReplacesARunningSandboxForAnyHarness(t *testing.T) {
	stubReceiptDir(t)
	previous := confirmStaleSandbox
	t.Cleanup(func() { confirmStaleSandbox = previous })
	cfg := launchCfg("s1")
	cfg.Name = "proveo-claudecode-abc"
	writeReceipt(cfg.Name, receiptOf(cfg, cfg.Image, []byte("k1"), "s1", "img"))
	now := receiptOf(cfg, cfg.Image, []byte("k2"), "s1", "img2")
	now.Image = cfg.Image
	var asked []string
	confirmStaleSandbox = func(name string, changed []string, live, clone, history bool) (bool, bool) {
		asked = append([]string{}, name)
		asked = append(asked, changed...)
		if !live {
			t.Errorf("live = false")
		}
		return true, true
	}
	retired := 0
	var said string
	_, err := retireIfStale(Input{Target: "claudecode"}, cfg, staleChanges(cfg.Name, now, true), func(string) bool { return true },
		func(Input, sbx.RunConfig, func(string, ...any)) error { retired++; return nil },
		func(f string, a ...any) { said += fmt.Sprintf(f, a...) + "\n" },
		func(string, ...string) {})
	if err != nil || retired != 1 {
		t.Fatalf("yes must replace a running sandbox: err=%v retired=%d", err, retired)
	}
	for _, want := range []string{"replacing proveo-claudecode-abc", "image"} {
		if !strings.Contains(said, want) && !strings.Contains(strings.Join(asked, " "), want) {
			t.Errorf("missing %q\nreport=%s\nasked=%v", want, said, asked)
		}
	}
}

func TestAHeadedNoKeepsAStoppedSandbox(t *testing.T) {
	stubReceiptDir(t)
	previous := confirmStaleSandbox
	t.Cleanup(func() { confirmStaleSandbox = previous })
	confirmStaleSandbox = func(string, []string, bool, bool, bool) (bool, bool) { return false, true }
	cfg := launchCfg("s1")
	writeReceipt(cfg.Name, receiptOf(cfg, cfg.Image, []byte("k1"), "s1", "img"))
	now := receiptOf(cfg, cfg.Image, []byte("k2"), "s1", "img")
	retired := 0
	var screen, log strings.Builder
	prev := ui.Default
	ui.Default = ui.New(&screen)
	restore := ui.TeeTo(&log)
	t.Cleanup(func() { restore(); ui.Default = prev })
	_, err := retireIfStale(Input{Target: "codex"}, cfg, staleChanges(cfg.Name, now, true), func(string) bool { return false },
		func(Input, sbx.RunConfig, func(string, ...any)) error { retired++; return nil }, func(string, ...any) {}, func(string, ...string) {})
	if err == nil || retired != 0 || !strings.Contains(err.Error(), "reattaches") || strings.Contains(err.Error(), "sbx rm") {
		t.Fatalf("no must keep the sandbox, and its error carries no how-to: err=%v retired=%d", err, retired)
	}
	if !strings.Contains(log.String(), "does not persist") || strings.Contains(screen.String(), "does not persist") {
		t.Errorf("the removal how-to belongs to the run log alone: the operator answered keep\nscreen:\n%s\nlog:\n%s", screen.String(), log.String())
	}
}

func TestAnOpenCodeEngineFollowsTheStaleSandboxRuleAndCarriesItsHistory(t *testing.T) {
	stubReceiptDir(t)
	previous := confirmStaleSandbox
	t.Cleanup(func() { confirmStaleSandbox = previous })
	var history bool
	confirmStaleSandbox = func(_ string, _ []string, _, _, h bool) (bool, bool) {
		history = h
		return true, true
	}
	cfg := launchCfg("s1")
	cfg.Name = "proveo-opencode-11c4aed0"
	writeReceipt(cfg.Name, receiptOf(cfg, cfg.Image, []byte("k1"), "s1", "img"))
	now := receiptOf(cfg, cfg.Image, []byte("k2"), "s1", "img")
	retired := 0
	_, err := retireIfStale(Input{Target: "opencode"}, cfg, staleChanges(cfg.Name, now, true), func(string) bool { return true },
		func(Input, sbx.RunConfig, func(string, ...any)) error { retired++; return nil }, func(string, ...any) {}, func(string, ...string) {})
	if err != nil || retired != 1 {
		t.Fatalf("a headed yes must replace a live OpenCode engine like any sandbox: err=%v retired=%d", err, retired)
	}
	if !history {
		t.Error("the upgrade prompt for an OpenCode engine does not say its history moves")
	}
}

func TestAFailedUpgradeScreenKeepsAStoppedSandbox(t *testing.T) {
	stubReceiptDir(t)
	previousHeaded, previousAsk := headedConfirm, askSandboxUpgrade
	t.Cleanup(func() {
		headedConfirm, askSandboxUpgrade = previousHeaded, previousAsk
	})
	headedConfirm = func() bool { return true }
	askSandboxUpgrade = func(string, []string, bool, bool, bool) (bool, bool) { return false, false }
	cfg := launchCfg("s1")
	writeReceipt(cfg.Name, receiptOf(cfg, cfg.Image, []byte("k1"), "s1", "img"))
	now := receiptOf(cfg, cfg.Image, []byte("k2"), "s1", "img")
	retired := 0
	_, err := retireIfStale(Input{Target: "cursor"}, cfg, staleChanges(cfg.Name, now, true), func(string) bool { return false },
		func(Input, sbx.RunConfig, func(string, ...any)) error { retired++; return nil }, func(string, ...any) {}, func(string, ...string) {})
	if err == nil || retired != 0 || !strings.Contains(err.Error(), "asks again") {
		t.Fatalf("a screen failure must count as no: err=%v retired=%d", err, retired)
	}
}

func TestReceiptRoundTrips(t *testing.T) {
	stubReceiptDir(t)
	r := receiptOf(launchCfg("s1", "A=1"), launchCfg("s1", "A=1").Image, []byte("k"), "s1", "img")
	writeReceipt("n", r)
	got := readReceipt("n")
	if got == nil || r.changes(got) != nil {
		t.Errorf("round trip lost data: %+v", got)
	}
	if readReceipt("missing") != nil {
		t.Error("a missing receipt must read as nil")
	}
}

func TestTheReceiptRecordsTheKitDirAndPublishingWithoutCountingThem(t *testing.T) {
	stubReceiptDir(t)
	cfg := sbx.RunConfig{Agent: "a", KitDir: "/s/runs/a/sbx/kit/proveo-a", Publish: []string{"56855:9222", "18083:8083"}}
	writeReceipt("sb", receiptOf(cfg, "img", nil, "", ""))
	was := readReceipt("sb")
	if was == nil || was.KitDir != cfg.KitDir || !slices.Equal(was.Publish, cfg.Publish) {
		t.Fatalf("receipt = %+v, want the kit dir and the publishing of its create", was)
	}
	next := receiptOf(sbx.RunConfig{Agent: "a", KitDir: "/s/runs/b/sbx/kit/proveo-a", Publish: []string{"18084:8083"}}, "img", nil, "", "")
	if got := next.changes(was); got != nil {
		t.Errorf("a new run's kit dir and port plan read as changes %q; the receipt only records them", got)
	}
	writeReceipt("none", receiptOf(sbx.RunConfig{Agent: "a"}, "img", nil, "", ""))
	if r := readReceipt("none"); r == nil || r.Publish == nil {
		t.Errorf("a create that published nothing must record an empty list, not an unknown one: %+v", r)
	}
}
