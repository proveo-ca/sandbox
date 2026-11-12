// SPEC: _spec/_plans/output-readability.puml, _spec/internal/sbx/clone-workspace.puml
package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/devports"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

// captured makes a plain printer the process printer and tees it to a log, for one test.
func captured(t *testing.T) (screen, log *strings.Builder) {
	t.Helper()
	screen, log = &strings.Builder{}, &strings.Builder{}
	prev := ui.Default
	ui.Default = ui.New(screen)
	restore := ui.TeeTo(log)
	t.Cleanup(func() { restore(); ui.Default = prev })
	return screen, log
}

func TestTheWorkspaceBlockSaysWhatSbxCannotAndEndsOnItsOutcome(t *testing.T) {
	screen, _ := captured(t)
	in := Input{Clone: true, RepoRoot: "/h/w/hotfix", CloneSource: workspace.WorktreeSource{Main: "/h/w/main", Ref: "dev"},
		CloneEnvHost: "/h/w/main/.env", CloneEnvDropped: []string{"ANTHROPIC_API_KEY"},
		Links: []workspace.Link{{Rel: "_spec", Target: "/h/sync/_spec", Action: workspace.LinkMounted}}}
	r := Report{OutputInClone: "reports", Unmounted: unmountable("/h/w/main", "main worktree", []string{"/h/w/main/.idea/a", "/h/w/main/.idea/b"})}
	ReportWorkspace(in, r, Absolute)
	want := []string{
		"------ workspace ------",
		"checkout: /h/w/hotfix (linked worktree)",
		"          cloned from /h/w/main at dev",
		"          uncommitted edits stay on the host",
		".env:     /h/w/main/.env (copy, rw)",
		"          edits merge back at teardown",
		"          leaves out ANTHROPIC_API_KEY, which sbx's proxy attaches",
		"output:   reports/ (in the clone)",
		"          lifted back at teardown",
		"links:    _spec → /h/sync/_spec",
		"warn: .idea/ — 2 entries inside the main worktree, not mounted",
		"ok: private clone — your checkout is not written",
		"    the agent's commits come back at teardown",
	}
	if got := strings.TrimSpace(screen.String()); !strings.HasSuffix(got, strings.Join(want, "\n")) {
		t.Errorf("the workspace block:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

func TestAMountedRunNamesWhyTheCloneDidNotApply(t *testing.T) {
	for _, c := range []struct {
		name, off, want string
	}{
		{"the default could not apply", "not a git repository — sbx clones with git", "ok: mounted checkout — the agent edits it directly\n    not a git repository — sbx clones with git\n"},
		{"--clone=false", "", "ok: mounted checkout — the agent edits it directly\n"},
	} {
		screen, _ := captured(t)
		ReportWorkspace(Input{CloneOff: c.off}, Report{}, Absolute)
		if got := screen.String(); !strings.HasSuffix(got, c.want) {
			t.Errorf("%s: the workspace block ends %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLiveWorkspacePathsAreHomeRelativeAndPrintPathsAbsolute(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	in := Input{Clone: true, CloneEnvHost: filepath.Join(home, "repo", ".env")}
	screen, _ := captured(t)
	ReportWorkspace(in, Report{}, ui.Tilde)
	if !strings.Contains(screen.String(), ".env: ~/repo/.env (copy, rw)") {
		t.Errorf("a live run's host path is not home-relative:\n%s", screen.String())
	}
	screen, _ = captured(t)
	ReportWorkspace(in, Report{}, Absolute)
	if !strings.Contains(screen.String(), ".env: "+filepath.Join(home, "repo", ".env")+" (copy, rw)") {
		t.Errorf("a --print path is not absolute:\n%s", screen.String())
	}
}

func TestACreatedSandboxLeavesSbxsOwnFactsToTheRunLog(t *testing.T) {
	screen, log := captured(t)
	in := Input{Image: "proveo/claudecode:latest", ProjectEnv: "/w/.env"}
	cfg := sbx.RunConfig{Name: "proveo-claudecode-1a2b3c4d", KitDir: "/state/sbx/kit/proveo-claudecode", CPUs: 4, Memory: "8g",
		Mounts: []sbx.Mount{{Host: "/w"}, {Host: "/h/.proveo/.claude", ReadOnly: true}}, Publish: []string{"18083:8083"}}
	reportCreate(in, cfg, 1, nil, nil)
	if got := screen.String(); got != "env:   1 value from /w/.env (--env-file)\nimage: proveo/claudecode:latest (published)\n" {
		t.Errorf("the screen before sbx create = %q, want the env seed, then proveo's image", got)
	}
	for _, want := range []string{"sandbox: proveo-claudecode-1a2b3c4d", "image: proveo/claudecode:latest", "kit: /state/sbx/kit/proveo-claudecode",
		"mount: /w (rw)", "mount: /h/.proveo/.claude (ro)", "cpus: 4", "memory: 8g", "publish: 18083:8083"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the run log lacks %q:\n%s", want, log.String())
		}
	}
}

func TestAReattachPrintsTheKeptSandboxsOwnRecordNotThisRunsSettings(t *testing.T) {
	screen, _ := captured(t)
	k := keptSandbox{name: "sb",
		listed:  &sbx.Listed{Name: "sb", Workspaces: []string{"/w", "/h/toolchains", "/h/skills:ro"}},
		receipt: &launchReceipt{Image: "proveo/claudecode:was", CPUs: 4, Memory: "8g", KitDir: "/state/runs/gone/sbx/kit/proveo-claudecode"}}
	reportReattach(Input{Image: "proveo/claudecode:this-run"}, k, 3, nil)
	want := "image:  proveo/claudecode:was\nkit:    /state/runs/gone/sbx/kit/proveo-claudecode\n        no longer on disk\n" +
		"mounts: /w\n        /h/toolchains\n        /h/skills (ro)\ncpus:   4\nmemory: 8g\nenv:    the values it was created with\n"
	if got := screen.String(); got != want {
		t.Errorf("the re-attach facts:\n%s\nwant:\n%s", got, want)
	}
}

func TestAReattachWithNoRecordClaimsNothingAndFlagsAKeptWorkspaceMode(t *testing.T) {
	screen, log := captured(t)
	reportReattach(Input{Image: "proveo/claudecode:this-run"}, keptSandbox{name: "sb", kept: []string{"clone", "image"}}, 0, nil)
	if got := screen.String(); got != "warn: workspace — sb keeps the mode it was created with\n" {
		t.Errorf("a re-attach with no listing and no receipt printed %q; want only the kept-mode warning", got)
	}
	for _, want := range []string{"sbx ls --json lists no sb", "the workspace block is this run's plan, not sb's"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the run log lacks %q:\n%s", want, log.String())
		}
	}
}

func TestAReattachToAnUnrecordedSandboxFlagsTheWorkspaceBlock(t *testing.T) {
	for _, c := range []struct {
		kept []string
		want string
	}{
		{[]string{noReceipt}, "warn: workspace — sb's mode and mounts are unrecorded"},
		{[]string{"mounts", "PROVEO_X"}, "warn: workspace — sb keeps the mounts it was created with"},
		{[]string{"image"}, ""},
	} {
		screen, _ := captured(t)
		reportReattach(Input{}, keptSandbox{name: "sb", kept: c.kept}, 0, nil)
		got := screen.String()
		if c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("kept %q printed %q, want %q", c.kept, got, c.want)
		}
	}
}

func TestTheStaleStepHandsOverOnlyWhenAStaleSandboxExists(t *testing.T) {
	orig := receiptDir
	t.Cleanup(func() { receiptDir = orig })
	dir := t.TempDir()
	receiptDir = func() string { return dir }
	now := launchReceipt{Agent: "claude", Image: "proveo/claudecode:latest"}
	if got := staleChanges("sb", now, false); got != nil {
		t.Errorf("no sandbox, changes = %q; want none, so the step stays a live line", got)
	}
	if got := staleChanges("sb", now, true); len(got) == 0 {
		t.Error("a sandbox with no receipt must count as stale, so the step can ask")
	}
	writeReceipt("sb", now)
	if got := staleChanges("sb", now, true); got != nil {
		t.Errorf("a sandbox created with these settings, changes = %q; want none", got)
	}
}

func TestTheReceiptRecordsCPUsAndMemoryWithoutMarkingASandboxStale(t *testing.T) {
	t.Parallel()
	was := receiptOf(sbx.RunConfig{Agent: "a", CPUs: 4, Memory: "8g"}, "img", []byte("k"), "", "")
	now := receiptOf(sbx.RunConfig{Agent: "a", CPUs: 8, Memory: "16g"}, "img", []byte("k"), "", "")
	if was.CPUs != 4 || was.Memory != "8g" {
		t.Errorf("receipt = %+v, want cpus 4 and memory 8g recorded", was)
	}
	if got := now.changes(&was); got != nil {
		t.Errorf("a cpus or memory change marked the sandbox stale: %q", got)
	}
}

func TestARetiredSandboxsRefsStayUnderTheSbxHeading(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "base"},
		{"commit-tree", "-p", "HEAD", "-m", "the retired agent's work", "HEAD^{tree}"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		if args[0] == "commit-tree" {
			if out, err := exec.Command("git", "-C", repo, "update-ref", sbx.CloneRefs("proveo-1-2-retired")+"/main", strings.TrimSpace(string(out))).CombinedOutput(); err != nil {
				t.Fatalf("update-ref: %v\n%s", err, out)
			}
		}
	}
	screen, log := captured(t)
	ui.Section(ui.SectionSbx)
	in := Input{RepoRoot: repo, Sid: "proveo-1-2-retired", ResultsDir: t.TempDir()}
	carried := NewBlock(SbxHeading)
	reportCloneRefs(in, sbx.RunConfig{Name: "sb"}, "", carried)
	carried.Print()
	got := screen.String()
	if !strings.Contains(got, "------ sbx ------") || strings.Contains(got, "results") {
		t.Errorf("a retired sandbox's refs drew another heading:\n%s", got)
	}
	if !strings.Contains(got, "branches: main (1 new commit)") || !strings.Contains(got, "under "+sbx.CloneRefs("proveo-1-2-retired")+"/") {
		t.Errorf("the branches fact is missing:\n%s", got)
	}
	if strings.Contains(got, "proveo adopt") || !strings.Contains(log.String(), "adopt: `proveo adopt proveo/proveo-1-2-retired/main`") {
		t.Errorf("the adopt hint belongs to the run log alone\nscreen:\n%s\nlog:\n%s", got, log.String())
	}
}

func TestTheStartLineNamesTheAgentAndItsVersion(t *testing.T) {
	t.Parallel()
	if got := agentStartLine(Input{Target: "claudecode", AgentVersion: "2.1.312"}); got != "claudecode 2.1.312 — starting" {
		t.Errorf("start line = %q", got)
	}
	if got := agentStartLine(Input{Target: "codex"}); got != "codex — starting" {
		t.Errorf("start line without a pinned version = %q", got)
	}
}

func TestARunThatFailsBeforeItsBlocksStillPrintsTheirItems(t *testing.T) {
	screen, log := captured(t)
	notADir := filepath.Join(t.TempDir(), "home")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	in := Input{HomeRoot: notADir, RunDir: t.TempDir(),
		Man:            manifest.Manifest{Home: manifest.Home{Enabled: true, Mounts: []manifest.HomeMount{{Host: ".claude"}}}},
		WorkspaceItems: []Item{{Warn: true, Text: ".env — not staged, the agent runs without it"}},
		InterfaceItems: []Item{{Warn: true, Text: "host-cdp — the agent controls every tab and session in that browser"}},
		InterfaceFacts: []ui.Fact{{Role: ui.RoleHost, Key: "chrome", Value: "CDP 127.0.0.1:9333 (reused)"}}}
	if err := Run(in); err == nil {
		t.Fatal("Run with an unpreparable proveo home succeeded")
	}
	for _, want := range []string{"------ workspace ------", ".env — not staged", "------ interface ------", "chrome: CDP 127.0.0.1:9333 (reused)", "host-cdp — the agent controls"} {
		if !strings.Contains(screen.String(), want) || !strings.Contains(log.String(), want) {
			t.Errorf("a run that failed before its blocks lost %q\nscreen:\n%s\nlog:\n%s", want, screen.String(), log.String())
		}
	}
}

func TestTheSecretsTheEnvSeedAndProveosImagePrintAsOneBlockBeforeSbxCreate(t *testing.T) {
	screen, _ := captured(t)
	secrets := []ui.Fact{
		secretFact("ANTHROPIC_API_KEY", "anthropic", "sb", nil, true),
		secretFact("OPENAI_API_KEY", "openai", "sb", []string{"--from-op"}, false),
	}
	reportCreate(Input{Image: "proveo/claudecode-browser:local", ProjectEnv: "/w/.env"}, sbx.RunConfig{Name: "sb"}, 2, secrets, nil)
	want := "secret: ANTHROPIC_API_KEY → sbx's \"anthropic\" entry, for sb only\n" +
		"          from its value\n" +
		"        OPENAI_API_KEY → sbx's \"openai\" entry, for sb only\n" +
		"          sbx resolves --from-op on the host\n" +
		"env:    2 values from /w/.env (--env-file)\n" +
		"image:  proveo/claudecode-browser:local (local build)\n"
	if got := screen.String(); got != want {
		t.Errorf("the sbx block before sbx create:\n%s\nwant:\n%s", got, want)
	}
}

func TestAReattachPrintsTheStoredSecretsInTheKeptSandboxsBlock(t *testing.T) {
	screen, _ := captured(t)
	k := keptSandbox{name: "sb", receipt: &launchReceipt{Image: "proveo/codex:latest"}}
	reportReattach(Input{}, k, 0, []ui.Fact{secretFact("OPENAI_API_KEY", "openai", "sb", nil, true)})
	want := "secret: OPENAI_API_KEY → sbx's \"openai\" entry, for sb only\n        from its value\nimage:  proveo/codex:latest\n"
	if got := screen.String(); got != want {
		t.Errorf("the re-attach block:\n%s\nwant:\n%s", got, want)
	}
}

func TestTheSbxInstallLineGivesEachVerbItsOwnIngForm(t *testing.T) {
	t.Parallel()
	for installed, want := range map[bool][2]string{false: {"install", "installing"}, true: {"upgrade", "upgrading"}} {
		if verb, doing := installVerb(installed); verb != want[0] || doing != want[1] {
			t.Errorf("installVerb(%v) = %q, %q; want %q, %q", installed, verb, doing, want[0], want[1])
		}
	}
}

func TestTheBuildxHowToReachesOnlyTheRunLog(t *testing.T) {
	screen, log := captured(t)
	_, notes := imageSource(Input{Image: "proveo/codex:local",
		ImageSource: func(ref string) sbx.ImageSource { return sbx.ImageSource{Ref: ref, Host: true} }})
	PrintNotes(Report{Notes: notes}, Absolute)
	if got := screen.String(); !strings.Contains(got, "buildx — no docker-driver builder reads the host image store") ||
		strings.Contains(got, "PROVEO_BUILDX_LOCAL_BUILDER") {
		t.Errorf("the screen names the warning and no knob:\n%s", got)
	}
	if !strings.Contains(log.String(), "PROVEO_BUILDX_LOCAL_BUILDER") {
		t.Errorf("the run log lacks the knob that chooses the builder:\n%s", log.String())
	}
}

func TestARetiredClonesLiftNamesTheOutputDirHomeRelative(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	screen, _ := captured(t)
	repo := filepath.Join(home, "w", "repo")
	in := Input{Clone: true, RepoRoot: repo, OutputDir: filepath.Join(repo, "reports")}
	_ = liftClonedOutput(in, sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: repo}}},
		func(context.Context, []string, string) (int, string, error) { return 0, "", nil }, nil)
	if got := screen.String(); !strings.Contains(got, "output: ~/w/repo/reports") || strings.Contains(got, home) {
		t.Errorf("the output fact names an absolute home path:\n%s", got)
	}
}

func TestEachHostAddOnIsOneFactInTheInterfaceBlock(t *testing.T) {
	screen, _ := captured(t)
	in := Input{
		InterfaceFacts: []ui.Fact{{Role: ui.RoleHost, Key: "android", Value: "emulator-5554 (AVD Pixel_9)", Detail: []string{"adb server 127.0.0.1:5037"}}, {}},
		InterfaceItems: []Item{{Warn: true, Text: "android — the agent controls every device on that adb server"}},
		AndroidApps:    []devports.AndroidApp{{Module: "app", AppID: "com.example"}},
	}
	reportInterface(in, sbx.RunConfig{Name: "sb"}, publishing{created: true})
	got := screen.String()
	for _, want := range []string{"------ interface ------\n", "android:     emulator-5554 (AVD Pixel_9)\n", "             adb server 127.0.0.1:5037\n",
		"android app: com.example\n", "warn: android — the agent controls every device on that adb server\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("the interface block lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "android app:") < strings.Index(got, "android:") || strings.Index(got, "warn:") < strings.Index(got, "android app:") {
		t.Errorf("the add-on fact, the apps, then the warning, in that order:\n%s", got)
	}
	screen.Reset()
	PrintInterfacePlan(Input{InterfaceFacts: in.InterfaceFacts})
	if got := screen.String(); !strings.Contains(got, "android: emulator-5554 (AVD Pixel_9)") {
		t.Errorf("--print's interface plan drops the add-on fact:\n%s", got)
	}
}

func TestAKeyLeftOutOfSbxsStoreSaysWhyOnlyInTheRunLog(t *testing.T) {
	keys := map[string]string{"OPENAI_API_KEY": "sk-test"}
	_, w, _ := Spec(Input{Target: "codex", Lookup: func(k string) string { return keys[k] }})
	screen, log := captured(t)
	PrintNotes(w.Report, Absolute)
	if got := screen.String(); got != "  sandbox secret: OPENAI_API_KEY — not stored\n" {
		t.Errorf("the screen = %q, want the note alone", got)
	}
	if !strings.Contains(log.String(), "`sbx secret set openai` stores OPENAI_API_KEY there to bill per token") {
		t.Errorf("the run log lacks the how-to:\n%s", log.String())
	}
}

func TestTheImageFactNamesAHostOnlyTagALocalBuild(t *testing.T) {
	for _, c := range []struct {
		name string
		src  *sbx.ImageSource
		want string
	}{
		{"a custom tag the host built", &sbx.ImageSource{Ref: "proveo/hermes:volatile-e2e", Host: true, Digest: "sha256:ab", Built: true}, "proveo/hermes:volatile-e2e (local build)"},
		{"a pulled image", &sbx.ImageSource{Ref: "proveo/hermes:latest", Host: true, Digest: "sha256:ab"}, "proveo/hermes:latest (published)"},
		{"no source: the :local tag", nil, "proveo/hermes:local (local build)"},
	} {
		image := "proveo/hermes:local"
		if c.src != nil {
			image = c.src.Ref
		}
		if got := imageFact(image, c.src).Value; got != c.want {
			t.Errorf("%s: image fact = %q, want %q", c.name, got, c.want)
		}
	}
}
