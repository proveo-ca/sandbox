// SPEC: _spec/internal/sbx/virtiofs-cwd-invalidation.puml
package sbx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClonePreservationTargetsRefsThatOutliveTheRemote(t *testing.T) {
	t.Parallel()
	const name = "proveo-1788208363-50815"
	if got := CloneRemote(name); got != "sandbox-"+name {
		t.Errorf("remote = %q; sbx names the clone's remote sandbox-<name>", got)
	}
	refs := CloneRefs(name)
	if strings.HasPrefix(refs, "refs/remotes/") {
		t.Errorf("%s: refs/remotes/<remote>/* is deleted with the remote, which `sbx rm` removes — the fetch has to land somewhere that survives", refs)
	}
	if !strings.HasPrefix(refs, "refs/proveo/") || !strings.HasSuffix(refs, name) {
		t.Errorf("refs = %q, want refs/proveo/<name>", refs)
	}

	fetch := strings.Join(CloneFetchArgs("/home/op/repo", name), " ")
	for _, want := range []string{"-C /home/op/repo", "fetch", "--no-tags", CloneRemote(name), "+refs/heads/*:" + refs + "/*"} {
		if !strings.Contains(fetch, want) {
			t.Errorf("fetch argv %q lacks %q", fetch, want)
		}
	}
}

func TestCloneLiftStreamsTheNestedDirFromTheCloneRoot(t *testing.T) {
	t.Parallel()
	args := CloneLiftArgs("sb", "/Users/op/my repo", "out/reports")
	joined := strings.Join(args, " ")
	if args[0] != "exec" || args[1] != "-w" || args[2] != "/" {
		t.Errorf("lift must exec with -w / — the container WorkingDir can stop resolving: %q", joined)
	}
	for _, want := range []string{
		`cd '/Users/op/my repo'`,  // host paths carry spaces; quoted, not split
		`[ -d 'out/reports' ] ||`, // absent dir is "nothing to lift", not a failure
		"exit 3",
		`tar -cf - 'out/reports'`, // archive members are relative, so the host unpacks under the repo root
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("lift argv %q lacks %q", joined, want)
		}
	}
	if CloneLiftNothing != 3 {
		t.Errorf("CloneLiftNothing = %d; the script above exits 3 for an absent dir", CloneLiftNothing)
	}
	if got := bashQuote(`it's`); got != `'it'\''s'` {
		t.Errorf("bashQuote(it's) = %s", got)
	}
}

func TestCloneSnapshotCommitsOnlyWhenSomethingIsStaged(t *testing.T) {
	t.Parallel()
	args := CloneSnapshotArgs("sb", "/Users/op/repo")
	joined := strings.Join(args, " ")
	if args[0] != "exec" || !strings.Contains(joined, "-w /Users/op/repo") {
		t.Errorf("snapshot must exec inside the clone's workdir: %q", joined)
	}
	if !strings.Contains(joined, "git add -A") || !strings.Contains(joined, "git diff --cached --quiet ||") {
		t.Errorf("snapshot must stage everything and commit only when the index is not empty: %q", joined)
	}
	if !strings.Contains(joined, "user.name=proveo") {
		t.Errorf("a teardown commit must say who made it: %q", joined)
	}
}

func TestBrowserViewportPinsChromiumsPortAndRelaysFromAnother(t *testing.T) {
	t.Parallel()
	if CDPRelayPort == CDPBrowserPort {
		t.Fatal("the relay cannot bind the port Chromium is already listening on")
	}
	if got := BrowserCDPArgs(""); got != "--no-sandbox,--remote-debugging-port=9223" {
		t.Errorf("BrowserCDPArgs(\"\") = %q", got)
	}
	if got := BrowserCDPArgs("--disable-gpu"); got != "--disable-gpu,--remote-debugging-port=9223" {
		t.Errorf("an operator's own args must survive: %q", got)
	}
	// An operator who pinned their own port keeps it: proveo would otherwise hand
	// Chromium two --remote-debugging-port flags and pick the loser.
	if got := BrowserCDPArgs("--remote-debugging-port=7000"); got != "--remote-debugging-port=7000" {
		t.Errorf("an explicit port must win: %q", got)
	}

	args := CDPRelayArgs("sb")
	joined := strings.Join(args, " ")
	if args[0] != "exec" || args[1] != "-w" || args[2] != "/" {
		t.Errorf("the relay must exec with -w / — the container WorkingDir can stop resolving: %q", joined)
	}
	for _, want := range []string{"python3", "9222", "9223", `bind(("0.0.0.0",L))`, `create_connection(("127.0.0.1",T))`} {
		if !strings.Contains(joined, want) {
			t.Errorf("relay argv lacks %q", want)
		}
	}
}

// -p is a creation-time flag, so it belongs with the other flags, ahead of the
// agent name and the positional workspaces.
func TestRunArgsEmitsPublishBeforeThePositionals(t *testing.T) {
	t.Parallel()
	args := RunArgs(RunConfig{Name: "sb", Agent: "claude", Publish: []string{"49999:9222"},
		Mounts: []Mount{{Host: "/w"}}})
	pub, agent := -1, -1
	for i, a := range args {
		switch a {
		case "-p":
			pub = i
		case "claude":
			agent = i
		}
	}
	if pub < 0 || args[pub+1] != "49999:9222" {
		t.Fatalf("no -p 49999:9222 in %v", args)
	}
	if pub > agent {
		t.Errorf("-p must precede the agent name; sbx parses the first positional as the agent: %v", args)
	}
}

// SPEC: _spec/internal/sbx/clone-workspace.puml
func TestCloneBundleStreamsFromTheCloneWithoutTheDaemon(t *testing.T) {
	t.Parallel()
	const tip = "87c459087afc24d69c98c58fae6e6b182012623f"
	args := CloneBundleArgs("sb", "/Users/op/my repo", []string{tip})
	joined := strings.Join(args, " ")

	if args[0] != "exec" || args[1] != "-w" || args[2] != "/" {
		t.Errorf("the bundle must exec with -w / like every other copy-out: %q", joined)
	}
	for _, want := range []string{
		`cd '/Users/op/my repo'`,
		"git cat-file -e",
		tip,
		`ex="$ex ^$c"`, // the exclusion is built at runtime, per tip the clone actually has
		"exit 4",
		"git bundle create --quiet - --branches",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("bundle script %q lacks %q", joined, want)
		}
	}
	if CloneBundleEmpty != 4 {
		t.Errorf("CloneBundleEmpty = %d; the script exits 4 when there is nothing new", CloneBundleEmpty)
	}
}

// The exclusion list is interpolated into a shell command, so anything that is
// not a plain object name is DROPPED rather than quoted.
func TestCloneBundleDropsAnythingThatIsNotAnObjectName(t *testing.T) {
	t.Parallel()
	args := CloneBundleArgs("sb", "/repo", []string{
		"87c459087afc24d69c98c58fae6e6b182012623f",
		"; rm -rf /",
		"$(whoami)",
		"refs/heads/main",
		"`id`",
		"deadbee",
	})
	script := args[len(args)-1]
	for _, forbidden := range []string{"rm -rf", "whoami", "refs/heads/main", "`id`", "$("} {
		if strings.Contains(script, forbidden) {
			t.Errorf("script carries %q:\n%s", forbidden, script)
		}
	}
	for _, want := range []string{"87c459087afc24d69c98c58fae6e6b182012623f", "deadbee"} {
		if !strings.Contains(script, want) {
			t.Errorf("script dropped the valid object name %q", want)
		}
	}
}

// `for c in ; do` is a syntax error, and a host whose tips cannot be read is an
// ordinary case — the bundle is then merely larger, not broken.
func TestCloneBundleWithNoExclusionsIsStillValidShell(t *testing.T) {
	t.Parallel()
	script := CloneBundleArgs("sb", "/repo", nil)[6]
	if strings.Contains(script, "for c in ;") {
		t.Fatalf("empty tips produced invalid shell:\n%s", script)
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n rejected the script (%v): %s\n%s", err, out, script)
	}
}

func TestCloneBundleFetchLandsInTheSameRefsAsTheDaemonPath(t *testing.T) {
	t.Parallel()
	got := strings.Join(CloneBundleFetchArgs("/home/op/repo", "/tmp/x.bundle", "sb"), " ")
	for _, want := range []string{"-C /home/op/repo", "fetch", "--no-tags", "/tmp/x.bundle",
		"+refs/heads/*:" + CloneRefs("sb") + "/*"} {
		if !strings.Contains(got, want) {
			t.Errorf("bundle fetch argv %q lacks %q", got, want)
		}
	}
	tips := strings.Join(CloneHostTipsArgs("/home/op/repo"), " ")
	if !strings.Contains(tips, "for-each-ref") || !strings.Contains(tips, "%(objectname)") {
		t.Errorf("host tips argv %q must list object names", tips)
	}
	if CloneHostTipsCap <= 0 {
		t.Error("the tip list must be bounded; it becomes an argv")
	}
}

// The script is shell proveo writes by hand, so it is run against real git —
// the one thing a string assertion cannot tell you is whether it works.
func TestCloneBundleScriptCarriesOnlyTheNewCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	host := filepath.Join(root, "host")
	clone := filepath.Join(root, "clone")

	git := func(dir string, args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	if out, err := exec.Command("git", "init", "-q", host).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(host, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(host, "add", "-A")
	git(host, "commit", "-qm", "base")
	if out, err := exec.Command("git", "clone", "-q", host, clone).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}

	// Nothing new yet: the sentinel, not an error.
	if code := runBundleScript(t, clone, hostTipsOf(t, host), filepath.Join(root, "empty.bundle")); code != CloneBundleEmpty {
		t.Fatalf("a clone with nothing new exited %d, want the %d sentinel", code, CloneBundleEmpty)
	}

	git(clone, "checkout", "-qb", "agent-work")
	if err := os.WriteFile(filepath.Join(clone, "a.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(clone, "commit", "-qam", "agent commit")

	bundle := filepath.Join(root, "work.bundle")
	if code := runBundleScript(t, clone, hostTipsOf(t, host), bundle); code != 0 {
		t.Fatalf("bundle script exited %d", code)
	}

	fetch := exec.Command("git", CloneBundleFetchArgs(host, bundle, "sb")...)
	if out, err := fetch.CombinedOutput(); err != nil {
		t.Fatalf("the host could not fetch the bundle (%v): %s", err, out)
	}
	refs := git(host, "for-each-ref", "--format=%(refname:short)", CloneRefs("sb")+"/")
	if !strings.Contains(refs, "proveo/sb/agent-work") {
		t.Fatalf("refs after the fetch = %q, want the agent's branch", refs)
	}
	if got := git(host, "log", "--oneline", "refs/proveo/sb/agent-work", "--not", "--branches"); !strings.Contains(got, "agent commit") {
		t.Fatalf("the agent's commit did not arrive: %q", got)
	}
}

func hostTipsOf(t *testing.T, repo string) []string {
	t.Helper()
	out, err := exec.Command("git", CloneHostTipsArgs(repo)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(out))
}

// runBundleScript executes exactly the shell CloneBundleArgs would hand sbx,
// with the container's workdir standing in for the clone.
func runBundleScript(t *testing.T, workdir string, tips []string, dest string) int {
	t.Helper()
	args := CloneBundleArgs("sb", workdir, tips)
	f, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	cmd := exec.Command("bash", "-c", args[len(args)-1])
	cmd.Stdout = f
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		t.Fatalf("running the bundle script: %v\n%s", err, errb.String())
	}
	return 0
}
