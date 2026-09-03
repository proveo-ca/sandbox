// SPEC: _spec/internal/sbx/virtiofs-cwd-invalidation.puml
package sbx

import (
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

// The lift is how a nested output dir reaches the host in clone mode: it cannot be
// mounted live (a positional under the repo lands inside the clone target and the
// clone is silently skipped), so it is streamed out at teardown.
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

// The browser viewport: two ports, because Chromium's DevTools endpoint refuses
// every peer that is not loopback — which is exactly what sbx's port forwarder
// is. Chromium stays on loopback; the relay owns the published port.
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
