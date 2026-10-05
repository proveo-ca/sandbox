// SPEC: _spec/internal/sbx/virtiofs-cwd-invalidation.puml, _spec/internal/sbx/clone-workspace.puml, _spec/internal/sbx/ide-attach.puml, _spec/defs/claudecode/chrome-bridge.puml
package sandbox

import (
	"errors"
	"github.com/proveo-ca/proveo/internal/devports"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/chromebridge"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

func TestSplitNestedKeepsTheRootAndItsSiblingsOnly(t *testing.T) {
	t.Parallel()
	root := "/Users/op/repo"
	mounts := []sbx.Mount{
		{Host: root},
		{Host: "/Users/op/repo/reports"},       // the output dir: nested
		{Host: "/Users/op/repo/data/fixtures"}, // a --data-dir inside the repo: nested
		{Host: "/Users/op/repo2"},              // shares a prefix, is not under root
		{Host: "/Users/op/Syncd/_spec"},        // a symlink target elsewhere
		{Host: "/Users/op/.proveo"},
	}
	kept, nested := SplitNested(root, mounts)
	wantKept := []sbx.Mount{{Host: root}, {Host: "/Users/op/repo2"}, {Host: "/Users/op/Syncd/_spec"}, {Host: "/Users/op/.proveo"}}
	if diff := cmp.Diff(wantKept, kept); diff != "" {
		t.Errorf("kept mismatch (-want +got):\n%s", diff)
	}
	wantNested := []sbx.Mount{{Host: "/Users/op/repo/reports"}, {Host: "/Users/op/repo/data/fixtures"}}
	if diff := cmp.Diff(wantNested, nested); diff != "" {
		t.Errorf("nested mismatch (-want +got):\n%s", diff)
	}
}

func TestNestedRelIsStrictAndSlashSeparated(t *testing.T) {
	t.Parallel()
	cases := []struct {
		root, path, want string
		ok               bool
	}{
		{"/r", "/r/reports", "reports", true},
		{"/r", "/r/out/reports/", "out/reports", true},
		{"/r/", "/r", "", false},
		{"/r", "/r", "", false},
		{"/r", "/r2/x", "", false},
		{"/r", "/", "", false},
		{"", "/r/x", "", false},
		{"/r", "", "", false},
	}
	for _, c := range cases {
		got, ok := nestedRel(c.root, c.path)
		if got != c.want || ok != c.ok {
			t.Errorf("nestedRel(%q, %q) = (%q, %v), want (%q, %v)", c.root, c.path, got, ok, c.want, c.ok)
		}
	}
}

func TestLiftClonedOutputTargetsTheRepoRootWithTheCloneRelativePath(t *testing.T) {
	t.Parallel()
	in := Input{Clone: true, RepoRoot: "/Users/op/repo", OutputDir: "/Users/op/repo/reports"}
	cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: "/Users/op/repo"}, {Host: "/Users/op/.proveo"}}}

	var gotArgs []string
	var gotInto string
	liftClonedOutput(in, cfg, func(args []string, into string) (int, string, error) {
		gotArgs, gotInto = args, into
		return 0, "", nil
	})
	if gotInto != "/Users/op/repo" {
		t.Errorf("unpacked under %q, want the repo root so members like reports/x land on the output dir", gotInto)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"exec -w / sb", "cd '/Users/op/repo'", "tar -cf - 'reports'"} {
		if !strings.Contains(joined, want) {
			t.Errorf("lift argv %q lacks %q", joined, want)
		}
	}

	called := false
	liftClonedOutput(Input{Clone: true, RepoRoot: "/Users/op/repo", OutputDir: "/Users/op/out"}, cfg,
		func([]string, string) (int, string, error) { called = true; return 0, "", nil })
	if called {
		t.Error("an output dir OUTSIDE the repo was mounted live; lifting it would be a second copy")
	}

	// "Nothing there" is a note, not a failure — it must not be reported as one.
	// The fake returns the sentinel with an error, the way exec does.
	liftClonedOutput(in, cfg, func([]string, string) (int, string, error) {
		return sbx.CloneLiftNothing, "", errors.New("exit status 3")
	})
}

func TestCDPPublishNeedsBothABrowserAndAPort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   Input
		want []string
	}{
		{"browser and port", Input{Browser: true, CDPHostPort: 49999}, []string{"49999:9222"}},
		{"no browser add-on", Input{CDPHostPort: 49999}, nil},
		{"no port (print mode)", Input{Browser: true}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if diff := cmp.Diff(c.want, cdpPublish(c.in)); diff != "" {
				t.Errorf("cdpPublish mismatch (-want +got):\n%s", diff)
			}
		})
	}
	if p := FreeLoopbackPort(); p <= 0 {
		t.Errorf("FreeLoopbackPort returned %d — the viewport needs a port before the sandbox exists", p)
	}
}

func TestCarryClonePicksTheTransportThatCanWork(t *testing.T) {
	t.Parallel()
	in := Input{Clone: true, RepoRoot: "/repo"}
	cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: "/repo"}}}

	ok := func(int, string, error) cloneCarry {
		return func(Input, sbx.RunConfig) (int, string, error) { return 0, "", nil }
	}(0, "", nil)
	fail := func(Input, sbx.RunConfig) (int, string, error) {
		return 1, "connection refused", errors.New("exit status 128")
	}
	empty := func(Input, sbx.RunConfig) (int, string, error) {
		return sbx.CloneBundleEmpty, "", errors.New("exit status 4")
	}

	cases := []struct {
		name       string
		running    bool
		remote     cloneCarry
		bundle     cloneCarry
		wantRemote bool
		wantBundle bool
		want       bool
	}{
		{"a running sandbox negotiates over its remote", true, ok, ok, true, false, true},
		{"a stopped sandbox goes straight to the bundle", false, ok, ok, false, true, true},
		{"a remote that refuses falls back to the bundle", true, fail, ok, true, true, true},
		{"nothing new is not a failure", false, ok, empty, false, true, false},
		{"both routes failing reports rather than pretends", false, fail, fail, false, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sawRemote, sawBundle bool
			remote := func(i Input, c sbx.RunConfig) (int, string, error) { sawRemote = true; return tc.remote(i, c) }
			bundle := func(i Input, c sbx.RunConfig) (int, string, error) { sawBundle = true; return tc.bundle(i, c) }

			if got := carryClone(in, cfg, tc.running, remote, bundle); got != tc.want {
				t.Errorf("carryClone = %v, want %v", got, tc.want)
			}
			if sawRemote != tc.wantRemote {
				t.Errorf("remote used = %v, want %v", sawRemote, tc.wantRemote)
			}
			if sawBundle != tc.wantBundle {
				t.Errorf("bundle used = %v, want %v", sawBundle, tc.wantBundle)
			}
		})
	}
}

func TestCloneRescueUsesATransportThatWorksOnAStoppedSandbox(t *testing.T) {
	t.Parallel()
	lines := CloneRescueLines("proveo-claudecode-1a2b3c4d", "proveo-1-2", "/host/repo", "/host/repo")
	joined := strings.Join(lines, "\n")

	if strings.Contains(joined, "git fetch sandbox-proveo-claudecode-1a2b3c4d") {
		t.Errorf("the recipe still reaches for the daemon that is not listening:\n%s", joined)
	}
	for _, want := range []string{"sbx exec -w / proveo-claudecode-1a2b3c4d", "bundle create", "git -C /host/repo fetch", "refs/proveo/proveo-1-2/*"} {
		if !strings.Contains(joined, want) {
			t.Errorf("rescue recipe lacks %q:\n%s", want, joined)
		}
	}
	if got := CloneRescueLines("sb", "sid", "", "/repo"); got != nil {
		t.Errorf("no workspace means no recipe to give, got %v", got)
	}
}

func TestIDEAttachOffersLiveAndNamesWhichTree(t *testing.T) {
	t.Parallel()
	cfg := sbx.RunConfig{Name: "proveo-1-2", Mounts: []sbx.Mount{{Host: "/host/repo"}}}

	live := strings.Join(IDEAttachLines(Input{Clone: true, RepoRoot: "/host/repo"}, cfg, true), "\n")
	for _, want := range []string{
		"IDE attach (live):",
		"sbx setup ssh",
		"proveo-1-2.sbx",
		"JetBrains: File → Remote Development → SSH → host `proveo-1-2.sbx`, use OpenSSH config, project /host/repo",
		"Toolbox (an IDE already installed in that environment): `jetbrains://gateway/ssh/environment?",
		"h=proveo-1-2.sbx",
		"projectHint=%2Fhost%2Frepo",
		"VS Code: `code --remote ssh-remote+proveo-1-2.sbx /host/repo`",
		"a local Open or Recent of /host/repo edits the HOST checkout, not this clone",
		"the running agent and the editor both write this tree",
		"DISPOSABLE CLONE",
		"commit IDE edits",
		"refs/proveo/proveo-1-2",
	} {
		if !strings.Contains(live, want) {
			t.Errorf("live clone attach guidance lacks %q:\n%s", want, live)
		}
	}
	for _, refuse := range []string{"agent exited", "one writer"} {
		if strings.Contains(live, refuse) {
			t.Errorf("live attach still treats two writers as a veto (%q):\n%s", refuse, live)
		}
	}

	kept := strings.Join(IDEAttachLines(Input{Clone: true, RepoRoot: "/host/repo"}, cfg, false), "\n")
	for _, want := range []string{"sbx setup ssh", "proveo-1-2.sbx", "DISPOSABLE CLONE"} {
		if !strings.Contains(kept, want) {
			t.Errorf("kept clone attach guidance lacks %q:\n%s", want, kept)
		}
	}
	if strings.Contains(kept, "(live)") {
		t.Errorf("kept attach still claims the agent is live:\n%s", kept)
	}
	if strings.Contains(kept, "both write this tree") {
		t.Errorf("kept attach still warns about a live agent:\n%s", kept)
	}

	direct := strings.Join(IDEAttachLines(Input{}, cfg, true), "\n")
	if !strings.Contains(direct, "mounted checkout") || !strings.Contains(direct, "write the host tree directly") {
		t.Errorf("direct attach guidance hides its write boundary:\n%s", direct)
	}
	if !strings.Contains(direct, "jetbrains://gateway/ssh/environment?") || !strings.Contains(direct, "code --remote ssh-remote+proveo-1-2.sbx") {
		t.Errorf("direct attach guidance lacks the remote launch targets:\n%s", direct)
	}
	if strings.Contains(direct, "edits the HOST checkout, not this clone") {
		t.Errorf("direct mode warned that a local open misses a clone it does not have:\n%s", direct)
	}
	if strings.Contains(direct, "DISPOSABLE") {
		t.Errorf("direct mode was described as a clone:\n%s", direct)
	}
}

func TestIDEAttachOpensTheLinkedWorktreeInAWorktreeClone(t *testing.T) {
	t.Parallel()
	in := Input{Clone: true, RepoRoot: "/host/worktrees/dev", CloneSource: workspace.WorktreeSource{Main: "/host/repo"}}
	cfg := sbx.RunConfig{Name: "proveo-1-2", Mounts: []sbx.Mount{{Host: "/host/repo"}}}

	got := strings.Join(IDEAttachLines(in, cfg, true), "\n")
	for _, want := range []string{
		"open /host/worktrees/dev",
		"host `proveo-1-2.sbx`, use OpenSSH config, project /host/worktrees/dev",
		"projectHint=%2Fhost%2Fworktrees%2Fdev",
		"code --remote ssh-remote+proveo-1-2.sbx /host/worktrees/dev",
		"a local Open or Recent of /host/worktrees/dev edits the HOST checkout",
		"linked worktree of /host/repo inside the clone",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("worktree clone attach guidance lacks %q:\n%s", want, got)
		}
	}

	plain := strings.Join(IDEAttachLines(Input{Clone: true, RepoRoot: "/host/repo"}, cfg, true), "\n")
	if strings.Contains(plain, "linked worktree") {
		t.Errorf("a main-checkout clone was described as a worktree:\n%s", plain)
	}
}

func TestIDEAttachNeedsANamedSandboxAndWorkspace(t *testing.T) {
	t.Parallel()
	for _, cfg := range []sbx.RunConfig{
		{Mounts: []sbx.Mount{{Host: "/repo"}}},
		{Name: "sb"},
	} {
		if got := IDEAttachLines(Input{}, cfg, true); got != nil {
			t.Errorf("IDEAttachLines(%+v) = %q, want no unusable offer", cfg, got)
		}
	}
}

func TestPrintIDEAttachLiveOpensTheInterfaceSection(t *testing.T) {
	var buf strings.Builder
	prev := ui.Default
	ui.Default = ui.New(&buf)
	t.Cleanup(func() { ui.Default = prev })

	PrintIDEAttach(Input{}, sbx.RunConfig{Name: "proveo-1-2", Mounts: []sbx.Mount{{Host: "/host/repo"}}}, true)
	got := buf.String()
	for _, want := range []string{"------ interface ------", "IDE attach (live):", "proveo-1-2.sbx", "sbx setup ssh"} {
		if !strings.Contains(got, want) {
			t.Errorf("live attach output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "------ starting ------") {
		t.Errorf("live attach drew the starting heading instead of interface:\n%s", got)
	}
}

func TestKitEnvVarsCarriesTheChromeBridgeToken(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "cb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	r, err := chromebridge.Start("127.0.0.1:0", dir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	vars := KitEnvVars(r.ResolvedEnv())
	if vars[chromebridge.EnvAddr] != r.ContainerAddr() || vars[chromebridge.EnvToken] != r.Token() {
		t.Fatalf("Kit variables = %v: the seed refuses %s without %s", vars, chromebridge.EnvAddr, chromebridge.EnvToken)
	}
}

func TestPlanPortsKeepsFreePortsAndMovesTakenOnes(t *testing.T) {
	t.Parallel()
	taken := map[int]bool{3000: true}
	got := PlanPorts([]PublishedPort{{Guest: 3000, What: "next"}, {Guest: 6006, What: "storybook"}},
		func(p int) bool { return !taken[p] })
	if got[1].Host != 6006 {
		t.Errorf("PlanPorts free 6006 = %+v, want host 6006", got[1])
	}
	if got[0].Guest != 3000 || got[0].Host == 3000 || got[0].Host <= 0 {
		t.Errorf("PlanPorts taken 3000 = %+v, want a different free host port", got[0])
	}
}

func TestDevServerScriptSkipsAPortAlreadyServedAndLogs(t *testing.T) {
	t.Parallel()
	got := DevServerScript("/work/repo", PublishedPort{Guest: 3000, Dir: "apps/web", Cmd: "HOST=0.0.0.0 PORT=3000 npm run dev -- -H 0.0.0.0"})
	want := "if (exec 3<>/dev/tcp/127.0.0.1/3000) 2>/dev/null; then exit 0; fi\n" +
		"cd \"/work/repo/apps/web\" || exit 1\n" +
		"exec HOST=0.0.0.0 PORT=3000 npm run dev -- -H 0.0.0.0 >>/tmp/proveo-dev-3000.log 2>&1\n"
	if got != want {
		t.Errorf("DevServerScript() =\n%s\nwant\n%s", got, want)
	}
}

func TestPortPublishAppendsAfterTheCDPRelay(t *testing.T) {
	t.Parallel()
	in := Input{Browser: true, CDPHostPort: 51000, Ports: []PublishedPort{{Host: 3000, Guest: 3000}, {Host: 51001, Guest: 6006}}}
	got := append(cdpPublish(in), portPublish(in)...)
	want := []string{"51000:9222", "3000:3000", "51001:6006"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("publish args mismatch (-want +got):\n%s", diff)
	}
}

func TestAndroidInstallScriptHandsTheModulesToTheSeed(t *testing.T) {
	t.Parallel()
	got := AndroidInstallScript("/work/repo", []string{"ADB_SERVER_SOCKET=tcp:host.docker.internal:5037"},
		[]devports.AndroidApp{{Module: ":app", AppID: "ca.proveo.hello"}, {AppID: "ca.proveo.root"}})
	want := "export ADB_SERVER_SOCKET=\"tcp:host.docker.internal:5037\"\n" +
		"cd \"/work/repo\" || exit 1\nsource /entrypoint-lib.sh\n" +
		"proveo_android_install \":app|ca.proveo.hello\" \"|ca.proveo.root\"\n"
	if got != want {
		t.Errorf("AndroidInstallScript() =\n%s\nwant\n%s", got, want)
	}
}
