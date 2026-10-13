// SPEC: _spec/internal/sbx/kit-lifecycle.puml
package sandbox

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/proveo-ca/proveo/internal/sbx"
)

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
		Env:    append([]string{"PROVEO_EGRESS_SESSION_ID=" + sid, "FILES=/state/egress/" + sid + "/home"}, env...),
	}
}

func TestReceiptIgnoresThePerRunSessionID(t *testing.T) {
	t.Parallel()
	kit := func(sid string) []byte { return []byte("kitDir: /state/egress/" + sid + "/sbx/kit\n") }
	a := receiptOf(launchCfg("proveo-1-1"), kit("proveo-1-1"), "proveo-1-1", "img1")
	b := receiptOf(launchCfg("proveo-2-2"), kit("proveo-2-2"), "proveo-2-2", "img1")
	if got := b.changes(&a); got != nil {
		t.Errorf("two runs differing only by session ID must match, got %v", got)
	}
}

func TestReceiptNamesWhatChanged(t *testing.T) {
	t.Parallel()
	was := receiptOf(launchCfg("s1", "OLLAMA_API_BASE=http://host.docker.internal:11434"), []byte("k1"), "s1", "img1")
	now := receiptOf(launchCfg("s2", "OLLAMA_API_BASE=http://host.docker.internal:11434", "PROVEO_HOST_CDP_PORT=9222"),
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
	old := receiptOf(cfg, []byte("k1"), "s1", "img")
	writeReceipt(cfg.Name, old)
	now := receiptOf(launchCfg("s2", "PROVEO_HOST_CDP_PORT=9222"), []byte("k2"), "s2", "img")

	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	retired := 0
	retire := func(Input, sbx.RunConfig) error { retired++; return nil }
	quiet := func(string, ...any) {}

	if err := retireIfStale(Input{}, cfg, now, no, no, retire, quiet, quiet); err != nil || retired != 0 {
		t.Errorf("no sandbox: err=%v retired=%d", err, retired)
	}
	same := receiptOf(launchCfg("s3"), []byte("k1"), "s3", "img")
	if err := retireIfStale(Input{}, cfg, same, yes, no, retire, quiet, quiet); err != nil || retired != 0 {
		t.Errorf("unchanged settings must re-attach: err=%v retired=%d", err, retired)
	}
	var warned string
	err := retireIfStale(Input{}, cfg, now, yes, yes, retire, quiet, func(f string, a ...any) { warned = fmt.Sprintf(f, a...) })
	if err != nil || retired != 0 || !strings.Contains(warned, "PROVEO_HOST_CDP_PORT") || !strings.Contains(warned, "NOT apply") {
		t.Errorf("a RUNNING changed sandbox is someone's live session: warn, never remove: err=%v retired=%d warn=%q", err, retired, warned)
	}
	var said string
	if err := retireIfStale(Input{}, cfg, now, yes, no, retire, func(f string, a ...any) { said += fmt.Sprintf(f, a...) + "\n" }, quiet); err != nil || retired != 1 {
		t.Errorf("a stopped changed sandbox must be re-created: err=%v retired=%d", err, retired)
	}
	for _, want := range []string{"carrying its work home", "sbx rm --force", "does not persist", "3/3"} {
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
	writeReceipt(cfg.Name, receiptOf(cfg, []byte("k1"), "s1", "img"))
	now := receiptOf(cfg, []byte("k2"), "s1", "img2")
	now.Image = cfg.Image
	var asked []string
	confirmStaleSandbox = func(name string, changed []string, live, clone bool) (bool, bool) {
		asked = append([]string{}, name)
		asked = append(asked, changed...)
		if !live {
			t.Errorf("live = false")
		}
		return true, true
	}
	retired := 0
	var said string
	err := retireIfStale(Input{Target: "claudecode"}, cfg, now, func(string) bool { return true }, func(string) bool { return true },
		func(Input, sbx.RunConfig) error { retired++; return nil },
		func(f string, a ...any) { said += fmt.Sprintf(f, a...) + "\n" },
		func(string, ...any) {})
	if err != nil || retired != 1 {
		t.Fatalf("yes must replace a running sandbox: err=%v retired=%d", err, retired)
	}
	for _, want := range []string{"proveo-claudecode-abc", "1/3", "2/3", "sbx rm --force", "does not persist", "3/3"} {
		if !strings.Contains(said, want) && !strings.Contains(strings.Join(asked, " "), want) {
			t.Errorf("missing %q\nreport=%s\nasked=%v", want, said, asked)
		}
	}
}

func TestAHeadedNoKeepsAStoppedSandbox(t *testing.T) {
	stubReceiptDir(t)
	previous := confirmStaleSandbox
	t.Cleanup(func() { confirmStaleSandbox = previous })
	confirmStaleSandbox = func(string, []string, bool, bool) (bool, bool) { return false, true }
	cfg := launchCfg("s1")
	writeReceipt(cfg.Name, receiptOf(cfg, []byte("k1"), "s1", "img"))
	now := receiptOf(cfg, []byte("k2"), "s1", "img")
	retired := 0
	err := retireIfStale(Input{Target: "codex"}, cfg, now, func(string) bool { return true }, func(string) bool { return false },
		func(Input, sbx.RunConfig) error { retired++; return nil }, func(string, ...any) {}, func(string, ...any) {})
	if err == nil || retired != 0 || !strings.Contains(err.Error(), "reattaches") || !strings.Contains(err.Error(), "does not persist") {
		t.Fatalf("no must keep the sandbox: err=%v retired=%d", err, retired)
	}
}

func TestAHeadedYesDoesNotRetireAnUnpublishedOpenCodeEngine(t *testing.T) {
	stubReceiptDir(t)
	previous := confirmStaleSandbox
	t.Cleanup(func() { confirmStaleSandbox = previous })
	asked := false
	confirmStaleSandbox = func(string, []string, bool, bool) (bool, bool) {
		asked = true
		return true, true
	}
	cfg := launchCfg("s1")
	cfg.Name = "proveo-opencode-11c4aed0"
	writeReceipt(cfg.Name, receiptOf(cfg, []byte("k1"), "s1", "img"))
	now := receiptOf(cfg, []byte("k2"), "s1", "img")
	retired := 0
	err := retireIfStale(Input{Target: "opencode"}, cfg, now, func(string) bool { return true }, func(string) bool { return true },
		func(Input, sbx.RunConfig) error { retired++; return nil }, func(string, ...any) {}, func(string, ...any) {})
	if err == nil || retired != 0 || asked || !strings.Contains(err.Error(), "unpublished or unattested") {
		t.Fatalf("unpublished OpenCode state must block replacement: err=%v retired=%d asked=%v", err, retired, asked)
	}
}

func TestAFailedUpgradeScreenKeepsAStoppedSandbox(t *testing.T) {
	stubReceiptDir(t)
	previousHeaded, previousScreen := headedConfirm, openTermScreen
	t.Cleanup(func() {
		headedConfirm, openTermScreen = previousHeaded, previousScreen
	})
	headedConfirm = func() bool { return true }
	openTermScreen = func() (tcell.Screen, error) { return nil, fmt.Errorf("no screen") }
	cfg := launchCfg("s1")
	writeReceipt(cfg.Name, receiptOf(cfg, []byte("k1"), "s1", "img"))
	now := receiptOf(cfg, []byte("k2"), "s1", "img")
	retired := 0
	err := retireIfStale(Input{Target: "cursor"}, cfg, now, func(string) bool { return true }, func(string) bool { return false },
		func(Input, sbx.RunConfig) error { retired++; return nil }, func(string, ...any) {}, func(string, ...any) {})
	if err == nil || retired != 0 || !strings.Contains(err.Error(), "asks again") {
		t.Fatalf("a screen failure must count as no: err=%v retired=%d", err, retired)
	}
}

func TestReceiptRoundTrips(t *testing.T) {
	stubReceiptDir(t)
	r := receiptOf(launchCfg("s1", "A=1"), []byte("k"), "s1", "img")
	writeReceipt("n", r)
	got := readReceipt("n")
	if got == nil || r.changes(got) != nil {
		t.Errorf("round trip lost data: %+v", got)
	}
	if readReceipt("missing") != nil {
		t.Error("a missing receipt must read as nil")
	}
}
