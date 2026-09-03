package sbx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// withProbes pins the availability seams for one test.
func withProbes(t *testing.T, path, wantGoos, wantGoarch string, kvmMissing bool) {
	t.Helper()
	oldLook, oldGoos, oldGoarch, oldKvm, oldStat := lookPath, goos, goarch, kvmDevice, stat
	// Available now gates on the CLI's version, so the version probe has to be
	// stubbed too or these cases would shell out to whatever sbx the host holds —
	// and "darwin arm64 is supported" would start depending on a real install.
	oldVer := sh.Version
	sh.Version = func() ([]byte, error) { return []byte("sbx version: v" + MinVersion + "\n"), nil }
	t.Cleanup(func() { sh.Version = oldVer })
	lookPath = func(string) (string, error) {
		if path == "" {
			return "", errors.New("not found")
		}
		return path, nil
	}
	goos, goarch = wantGoos, wantGoarch
	if kvmMissing {
		stat = func(string) (os.FileInfo, error) { return nil, errors.New("missing") }
	} else {
		stat = func(string) (os.FileInfo, error) { return nil, nil }
	}
	t.Cleanup(func() {
		lookPath, goos, goarch, kvmDevice, stat = oldLook, oldGoos, oldGoarch, oldKvm, oldStat
	})
}

func TestAvailableOKPaths(t *testing.T) {
	cases := []struct {
		name            string
		path            string
		goos, goarch    string
		kvmMissing      bool
		wantOK          bool
		wantWhyContains string
	}{
		{"darwin arm64", "/usr/local/bin/sbx", "darwin", "arm64", false, true, ""},
		{"linux with kvm", "/usr/bin/sbx", "linux", "amd64", false, true, ""},
		{"no cli", "", "darwin", "arm64", false, false, "CLI not found"},
		{"linux no kvm", "/usr/bin/sbx", "linux", "amd64", true, false, "KVM"},
		{"windows", "/x/sbx", "windows", "arm64", false, false, "unsupported platform"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withProbes(t, tc.path, tc.goos, tc.goarch, tc.kvmMissing)
			ok, why := Available()
			if ok != tc.wantOK {
				t.Fatalf("Available() ok=%v why=%q, want ok=%v", ok, why, tc.wantOK)
			}
			if tc.wantWhyContains != "" && !strings.Contains(why, tc.wantWhyContains) {
				t.Fatalf("why=%q, want contains %q", why, tc.wantWhyContains)
			}
		})
	}
}

// The v0.39 argv, pinned shape-for-shape. Every difference from a docker-style
// invocation here is one that failed a real run, one flag at a time: `-v` is
// --cloud-only, `-w` does not exist, and an image in the first positional is read
// as an unknown agent name.
func TestRunArgsFull(t *testing.T) {
	got := RunArgs(RunConfig{
		Name:   "proveo-1-2",
		Agent:  "claude",
		KitDir: "/state/egress/x/kit",
		Image:  "proveo/claudecode:latest",
		Mounts: []Mount{
			{Host: "/repo", Container: "/app"},
			{Host: "/out", Container: "/app/output", ReadOnly: true},
		},
		Env:     []string{"PROVEO_EVIDENCE=concise"},
		Command: []string{"-p", "hello"},
	})
	want := []string{
		"run", "--name", "proveo-1-2", "--kit", "/state/egress/x/kit",
		"-t", "proveo/claudecode:latest",
		"-e", "PROVEO_EVIDENCE=concise",
		"claude",  // AGENT: the first positional, and mandatory
		"/repo",   // workspaces follow, at their HOST paths
		"/out:ro", // :ro is the only modifier a workspace takes
		"--", "-p", "hello",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RunArgs()=\n%q\nwant\n%q", got, want)
	}
}

// Teardown has to be non-interactive or it blocks on a confirmation prompt.
func TestRemoveArgsForcesNonInteractively(t *testing.T) {
	t.Parallel()
	got := RemoveArgs("proveo-1-2")
	want := []string{"rm", "--force", "proveo-1-2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RemoveArgs() = %q, want %q", got, want)
	}
}

// A run that failed before creating anything must not be reported as a failed
// teardown — the real error is the one `sbx run` already printed.
func TestNotFoundRecognisesAnAbsentSandbox(t *testing.T) {
	t.Parallel()
	if !NotFound("Error: sandbox 'proveo-1-2' not found (run 'sbx ls' to see your sandboxes)") {
		t.Error("the CLI's own not-found wording must be recognised")
	}
	if NotFound("Error: permission denied") {
		t.Error("an unrelated failure must still be reported")
	}
}

func TestRunArgsMinimal(t *testing.T) {
	got := RunArgs(RunConfig{})
	want := []string{"run"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RunArgs()=%q, want %q", got, want)
	}
}

func TestSecretSetArgs(t *testing.T) {
	// --force, or a re-run cancels its own write: sbx asks to overwrite an existing
	// secret and the piped value answers the prompt rather than becoming the secret.
	if got, want := SecretSetArgs("CLAUDE_CODE_OAUTH_TOKEN"), []string{"secret", "set", "--force", "CLAUDE_CODE_OAUTH_TOKEN"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SecretSetArgs=%q want %q", got, want)
	}
}

func TestSecretSetPipesValueViaStdin(t *testing.T) {
	var gotName, gotValue string
	old := sh.SecretSet
	sh.SecretSet = func(name, value string) error {
		gotName, gotValue = name, value
		return nil
	}
	t.Cleanup(func() { sh.SecretSet = old })
	if err := SecretSet("TOK", "sekret"); err != nil {
		t.Fatal(err)
	}
	if gotName != "TOK" || gotValue != "sekret" {
		t.Fatalf("SecretSet delivered name=%q value=%q", gotName, gotValue)
	}
}

func TestWriteKitRendersAMixinNotASandbox(t *testing.T) {
	dir := t.TempDir()
	kitDir, err := WriteKit(filepath.Join(dir, "kit"), Kit{
		SchemaVersion: KitSchemaVersionV2,
		Kind:          "mixin",
		Name:          "claudecode-posture",
		Permissions: KitPermissions{Network: KitNet{
			Allow: []string{"api.anthropic.com", "statsig.anthropic.com"},
		}},
		Environment: &KitEnv{Variables: map[string]string{
			"ANTHROPIC_MODEL": "claude-opus-5",
			"PROVEO_WORKDIR":  "/w",
		}},
		Setup: &KitSetup{Startup: []KitCommand{SeedCommand("claudecode")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(kitDir, "spec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)

	// schemaVersion is a STRING per SPEC-v2, and the Kit declares no agent: sbx's
	// agent list is closed, so a `kind: sandbox` Kit gets no artifact and its
	// session is dropped seconds in.
	for _, want := range []string{
		`schemaVersion: "2"`, "kind: mixin", "name: claudecode-posture",
		"allow:", "- api.anthropic.com",
		"environment:", "ANTHROPIC_MODEL: claude-opus-5",
		"setup:", "startup:", "/usr/local/bin/proveo-seed",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("spec.yaml missing %q:\n%s", want, s)
		}
	}
	// A mixin must declare NO agent, NO image, and NO credentials: the built-in
	// agent owns all three, and repeating a credential service is rejected
	// outright ("defined in both").
	for _, banned := range []string{"sandbox:", "image:", "entrypoint:", "credentials:"} {
		if strings.Contains(s, banned) {
			t.Errorf("a mixin must not declare %q:\n%s", banned, s)
		}
	}
	if strings.Contains(s, "sk-") {
		t.Errorf("spec.yaml carries something secret-shaped:\n%s", s)
	}
	fi, err := os.Stat(filepath.Join(kitDir, "spec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("spec.yaml mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestWriteKitOmitsEmptyNetwork(t *testing.T) {
	dir := t.TempDir()
	kitDir, err := WriteKit(dir, Kit{
		SchemaVersion: KitSchemaVersionV2,
		Kind:          "mixin",
		Name:          "cursor-posture",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(kitDir, "spec.yaml"))
	// cursor declares no allowlist and no brokered credential (it pins its TLS, so
	// there is nothing to inject into) — the blocks must be absent, not empty, or
	// the spec declares a policy it does not have.
	for _, absent := range []string{"allow:", "credentials:"} {
		if strings.Contains(string(b), absent) {
			t.Errorf("an unset block should be omitted, found %q:\n%s", absent, b)
		}
	}
}

func TestInstallHintIsPlatformSpecific(t *testing.T) {
	origOS, origArch := goos, goarch
	t.Cleanup(func() { goos, goarch = origOS, origArch })

	tests := []struct {
		os, arch string
		want     string // substring; "" => no hint at all
	}{
		{"darwin", "arm64", "brew install docker/tap/sbx"},
		{"darwin", "amd64", ""},
		{"windows", "amd64", "winget install -h Docker.sbx"},
		{"linux", "amd64", "docker-sbx"},
		{"freebsd", "amd64", ""},
	}
	for _, tc := range tests {
		t.Run(tc.os+"/"+tc.arch, func(t *testing.T) {
			goos, goarch = tc.os, tc.arch
			got := InstallHint()
			if tc.want == "" {
				if got != "" {
					t.Errorf("InstallHint() = %q, want no hint on %s/%s", got, tc.os, tc.arch)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("InstallHint() = %q, want it to contain %q", got, tc.want)
			}
			if strings.Contains(got, "Docker Desktop") {
				t.Errorf("InstallHint() = %q, must not imply Docker Desktop is required", got)
			}
		})
	}
}

// proveo owns the sbx version, so the parse has to survive the CLI's own
// wording: `sbx version` answers "sbx version: v0.39.0 <sha>", not a bare
// semver, and `--version` is not a flag it accepts at all.
func TestVersionParsesTheCLIsOwnWording(t *testing.T) {
	orig := sh.Version
	t.Cleanup(func() { sh.Version = orig })

	for _, tc := range []struct{ out, want string }{
		{"sbx version: v0.39.0 def8cb0523a77e757bdd6ef52b459fe374f3783e\n", "0.39.0"},
		{"sbx version: v0.35.0\n", "0.35.0"},
		{"0.40.1\n", "0.40.1"},
	} {
		sh.Version = func() ([]byte, error) { return []byte(tc.out), nil }
		got, err := Version()
		if err != nil || got != tc.want {
			t.Errorf("Version() from %q = %q, %v; want %q", tc.out, got, err, tc.want)
		}
	}

	sh.Version = func() ([]byte, error) { return []byte("nothing here"), nil }
	if _, err := Version(); err == nil {
		t.Error("a version-less answer must be an error, not an empty string treated as old")
	}
}

func TestOlderOrdersVersionsAndFailsOpen(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		got, want string
		older     bool
	}{
		{"0.35.0", "0.39.0", true},
		{"0.39.0", "0.39.0", false},
		{"0.40.0", "0.39.0", false},
		{"1.0.0", "0.39.0", false},
		{"0.39.1", "0.39.0", false},
		{"0.8.0", "0.39.0", true}, // numeric, not lexical: 8 < 39
		// A scheme this build has never seen is assumed NEWER. Blocking a host the
		// operator just upgraded would be the worse failure of the two.
		{"weird", "0.39.0", false},
		{"0.39.0", "weird", false},
	} {
		if got := Older(tc.got, tc.want); got != tc.older {
			t.Errorf("Older(%q, %q) = %v, want %v", tc.got, tc.want, got, tc.older)
		}
	}
}

// The install line and the upgrade line are different commands, and handing an
// operator "brew install" for an sbx they already have is how a version gate
// turns into a no-op they follow twice.
func TestInstallCmdDistinguishesInstallFromUpgrade(t *testing.T) {
	origOS, origArch := goos, goarch
	t.Cleanup(func() { goos, goarch = origOS, origArch })

	goos, goarch = "darwin", "arm64"
	fresh, upgrade := InstallCmd(false), InstallCmd(true)
	if fresh == upgrade {
		t.Fatalf("install and upgrade must differ, both = %q", fresh)
	}
	if !strings.Contains(fresh, "install") || !strings.Contains(upgrade, "upgrade") {
		t.Errorf("install = %q, upgrade = %q", fresh, upgrade)
	}

	goos, goarch = "darwin", "amd64"
	if got := InstallCmd(false); got != "" {
		t.Errorf("darwin/amd64 has no sbx route, got %q", got)
	}
	goos, goarch = "plan9", "arm64"
	if got := InstallCmd(false); got != "" {
		t.Errorf("unsupported platform must offer nothing, got %q", got)
	}
}

// Available gates on the version, because every drift this pin exists for fails
// deep inside a run instead of at selection time.
func TestAvailableRejectsATooOldCLI(t *testing.T) {
	origOS, origArch, origLook, origVer := goos, goarch, lookPath, sh.Version
	t.Cleanup(func() { goos, goarch, lookPath, sh.Version = origOS, origArch, origLook, origVer })

	goos, goarch = "darwin", "arm64"
	lookPath = func(string) (string, error) { return "/usr/local/bin/sbx", nil }

	sh.Version = func() ([]byte, error) { return []byte("sbx version: v0.35.0\n"), nil }
	ok, why := Available()
	if ok {
		t.Error("a CLI older than MinVersion must not be selected")
	}
	if !strings.Contains(why, MinVersion) {
		t.Errorf("the reason must name the version proveo targets, got %q", why)
	}

	sh.Version = func() ([]byte, error) { return []byte("sbx version: v" + MinVersion + "\n"), nil }
	if ok, why := Available(); !ok {
		t.Errorf("MinVersion exactly must be accepted, got %q", why)
	}
}

// The sandbox runtime's image store is separate from the host engine's, so a run
// has to hand the image over. It must also NOT hand it over twice: these images
// are multi-GB and `docker save | sbx template load` is the slowest step in a
// sandbox run by a wide margin.
func TestEnsureTemplateSkipsAnImageAlreadyLoaded(t *testing.T) {
	origList, origLoad := sh.TemplateList, sh.TemplateLoad
	t.Cleanup(func() { sh.TemplateList, sh.TemplateLoad = origList, origLoad })

	loads := 0
	sh.TemplateLoad = func(string) error { loads++; return nil }
	stubIDs(t, map[string]string{"proveo/claudecode:latest": "aaaaaaaaaaaa"})
	stubReceipts(t, map[string]string{"proveo/claudecode:latest": "aaaaaaaaaaaa"})

	sh.TemplateList = func() ([]byte, error) {
		return []byte("REPOSITORY              TAG\nproveo/claudecode       latest\n"), nil
	}
	if err := EnsureTemplate("proveo/claudecode:latest", nil); err != nil {
		t.Fatalf("EnsureTemplate: %v", err)
	}
	if loads != 0 {
		t.Errorf("an image already in the store was loaded again (%d times)", loads)
	}

	sh.TemplateList = func() ([]byte, error) { return []byte("REPOSITORY  TAG\n"), nil }
	if err := EnsureTemplate("proveo/claudecode:latest", nil); err != nil {
		t.Fatalf("EnsureTemplate: %v", err)
	}
	if loads != 1 {
		t.Errorf("a missing image was loaded %d times, want exactly 1", loads)
	}

	// An unreadable store is treated as "not present": loading again is wasteful,
	// but running against an image that is genuinely absent fails the whole run.
	sh.TemplateList = func() ([]byte, error) { return nil, errors.New("daemon down") }
	if HasTemplate("proveo/claudecode:latest") {
		t.Error("an unreadable store must not report the image as present")
	}
}

// An empty image is not an error to load — it is nothing to load.
func TestEnsureTemplateIgnoresAnEmptyImage(t *testing.T) {
	origLoad := sh.TemplateLoad
	t.Cleanup(func() { sh.TemplateLoad = origLoad })
	sh.TemplateLoad = func(string) error { t.Fatal("empty image must not reach the loader"); return nil }
	if err := EnsureTemplate("", nil); err != nil {
		t.Errorf("EnsureTemplate(\"\") = %v, want nil", err)
	}
}

// The store's real output, verbatim from `sbx template ls` on a host that had
// just loaded one image. Columns, a registry qualifier, and a blank FLAVOR — all
// three broke the first matcher this replaced.
const realTemplateLS = `REPOSITORY                           TAG            IMAGE ID       FLAVOR         CREATED
docker.io/docker/sandbox-templates   shell-docker   d86a6cdc105a   shell-docker   46 minutes ago
docker.io/proveo/egress-proxy        latest         4ee370d17e72                  Less than a minute ago
`

// stubIDs makes localImageID answer from a map, so identity can be varied without
// a host engine.
func stubIDs(t *testing.T, ids map[string]string) {
	t.Helper()
	orig := sh.LocalImageID
	t.Cleanup(func() { sh.LocalImageID = orig })
	sh.LocalImageID = func(image string) string { return ids[image] }
}

// stubReceipts points the receipt dir at a temp dir and pre-records the given
// loads, standing in for images this host loaded on an earlier run. Identity is
// read from here rather than from the store's IMAGE ID column, because `sbx
// create` rewrites that column to the ID of the image it bakes.
func stubReceipts(t *testing.T, loaded map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	orig := templateReceiptDir
	t.Cleanup(func() { templateReceiptDir = orig })
	templateReceiptDir = func() string { return dir }
	for image, id := range loaded {
		if err := os.WriteFile(receiptFile(image), []byte(id), 0o644); err != nil {
			t.Fatalf("seed receipt for %s: %v", image, err)
		}
	}
	return dir
}

// A REBUILT :latest has the same reference and a different ID. Matching on
// reference alone reported it present, so the store kept serving the image it
// received first — which is how a sandbox came up with no /app after the
// workspace layout was standardised and the images rebuilt.
func TestHasTemplateReloadsARebuiltImage(t *testing.T) {
	orig := sh.TemplateList
	t.Cleanup(func() { sh.TemplateList = orig })
	sh.TemplateList = func() ([]byte, error) { return []byte(realTemplateLS), nil }

	// What proveo last handed to the store, recorded at load time.
	stubReceipts(t, map[string]string{"proveo/egress-proxy:latest": "4ee370d17e72"})

	stubIDs(t, map[string]string{"proveo/egress-proxy:latest": "4ee370d17e72"})
	if !HasTemplate("proveo/egress-proxy:latest") {
		t.Error("same reference AND same id must count as present")
	}

	stubIDs(t, map[string]string{"proveo/egress-proxy:latest": "ffffffffffff"})
	if HasTemplate("proveo/egress-proxy:latest") {
		t.Error("a rebuilt image (same ref, new id) must NOT count as present")
	}

	// Not built locally: identity is unknowable, so presence is the best answer.
	stubIDs(t, map[string]string{})
	if !HasTemplate("proveo/egress-proxy:latest") {
		t.Error("with no local image to compare, presence must still count")
	}

	// The store's own IMAGE ID column must NOT be what identity is read from: one
	// `sbx create` re-bakes the template and rewrites that column, so a run that
	// trusted it would reload a multi-GB tar on every launch forever after.
	stubIDs(t, map[string]string{"proveo/egress-proxy:latest": "4ee370d17e72"})
	stubReceipts(t, map[string]string{"proveo/egress-proxy:latest": "4ee370d17e72"})
	baked := `REPOSITORY                       TAG      IMAGE ID       FLAVOR   CREATED
docker.io/proveo/egress-proxy    latest   5fcb2266417f            1 minute ago
`
	sh.TemplateList = func() ([]byte, error) { return []byte(baked), nil }
	if !HasTemplate("proveo/egress-proxy:latest") {
		t.Error("a template sbx re-baked after loading must still count as loaded")
	}
}

func TestHasTemplateReadsTheStoresRealColumns(t *testing.T) {
	orig := sh.TemplateList
	t.Cleanup(func() { sh.TemplateList = orig })
	sh.TemplateList = func() ([]byte, error) { return []byte(realTemplateLS), nil }
	// Identity is a separate axis (TestHasTemplateReloadsARebuiltImage); here the
	// ids are made to agree so the columns are what is under test.
	ids := map[string]string{
		"proveo/egress-proxy:latest":            "4ee370d17e72",
		"docker.io/proveo/egress-proxy:latest":  "4ee370d17e72",
		"proveo/egress-proxy":                   "4ee370d17e72",
		"docker/sandbox-templates:shell-docker": "d86a6cdc105a",
	}
	stubIDs(t, ids)
	stubReceipts(t, ids)

	for _, tc := range []struct {
		image string
		want  bool
	}{
		// Present, despite the store qualifying it with a registry.
		{"proveo/egress-proxy:latest", true},
		{"docker.io/proveo/egress-proxy:latest", true},
		{"proveo/egress-proxy", true}, // bare ref means :latest, as docker does
		{"docker/sandbox-templates:shell-docker", true},
		// Absent — and the TAG is why. A repository-only match would call this
		// present and then run the agent on a stale image forever.
		{"proveo/egress-proxy:v2", false},
		{"proveo/claudecode:latest", false},
		{"proveo/claudecode-browser:latest", false},
	} {
		if got := HasTemplate(tc.image); got != tc.want {
			t.Errorf("HasTemplate(%q) = %v, want %v", tc.image, got, tc.want)
		}
	}
}

// A Kit that declares an agent sbx already ships is refused outright — `agent
// "cursor" is already registered (built-in agents cannot be overridden by a
// kit)` — and the refusal is quiet: the session dies before the shell with
// nothing on screen. Two of proveo's four defs are named after built-ins, so the
// namespacing is what makes them runnable at all.
func TestAgentNameNeverCollidesWithAnSbxBuiltin(t *testing.T) {
	t.Parallel()
	builtin := map[string]bool{}
	for _, b := range BuiltinAgents {
		builtin[b] = true
	}
	// The two that made this fail in practice, plus the ones that did not: a def
	// escaping the prefix would land back on a built-in without warning.
	for _, target := range []string{"cursor", "opencode", "claudecode", "cecli"} {
		got := AgentName(target)
		if builtin[got] {
			t.Errorf("AgentName(%q) = %q, which sbx already registers as a built-in agent", target, got)
		}
		if got == target {
			t.Errorf("AgentName(%q) returned the bare target — nothing separates it from a built-in", target)
		}
	}
}

// A sandbox that never started is worth retrying on a fresh template; an agent
// that ran and exited non-zero is not, because the retry would run it twice.
func TestExistsSeparatesAColdSandboxFromAFailedAgent(t *testing.T) {
	orig := sh.SandboxList
	t.Cleanup(func() { sh.SandboxList = orig })

	sh.SandboxList = func() ([]byte, error) {
		return []byte("NAME                     AGENT          STATUS\nproveo-1787-1  proveo-cursor  running\n"), nil
	}
	if !Exists("proveo-1787-1") {
		t.Error("a sandbox present in the listing must count as existing — its agent ran")
	}
	if Exists("proveo-1787-2") {
		t.Error("a name absent from the listing must not count as existing")
	}
	if Exists("") {
		t.Error("an unnamed sandbox cannot exist")
	}

	// No listing at all: the safe answer is "it exists", because the alternative
	// is retrying a run whose agent may already have done its work.
	sh.SandboxList = func() ([]byte, error) { return nil, errors.New("daemon down") }
	if !Exists("proveo-1787-1") {
		t.Error("an unreadable listing must not license a retry")
	}
}

// The repair path must load unconditionally: it runs precisely when a receipt
// already says the image is current and the template is nonetheless unusable.
func TestReloadTemplateLoadsEvenWithAMatchingReceipt(t *testing.T) {
	origLoad, origList := sh.TemplateLoad, sh.TemplateList
	t.Cleanup(func() { sh.TemplateLoad, sh.TemplateList = origLoad, origList })
	loads := 0
	// A store that TAKES the image: after the load it prints the id it was handed.
	// The previous fixture never updated, which is exactly the pathology
	// confirmLoaded now catches, so it would fail the post-load verification.
	loaded := false
	sh.TemplateLoad = func(string) error { loads++; loaded = true; return nil }
	sh.TemplateList = func() ([]byte, error) {
		id := "000000000000"
		if loaded {
			id = "abcabcabcabc"
		}
		return []byte("REPOSITORY                  TAG      IMAGE ID       FLAVOR   CREATED\n" +
			"docker.io/proveo/cursor     latest   " + id + "            1 minute ago\n"), nil
	}
	stubIDs(t, map[string]string{"proveo/cursor:latest": "abcabcabcabc"})
	stubReceipts(t, map[string]string{"proveo/cursor:latest": "abcabcabcabc"})

	if err := ReloadTemplate("proveo/cursor:latest", nil); err != nil {
		t.Fatalf("ReloadTemplate: %v", err)
	}
	if loads != 1 {
		t.Errorf("loads = %d, want 1: the repair must not consult the receipt it is repairing past", loads)
	}
	if err := ReloadTemplate("", nil); err != nil || loads != 1 {
		t.Errorf("an empty image must be a no-op, got err=%v loads=%d", err, loads)
	}
}

var errNoDaemon = errors.New("cannot connect to the docker daemon")

func TestMemoryLimitDerivesFromDaemonNotHost(t *testing.T) {
	orig := sh.DockerMemTotal
	defer func() { sh.DockerMemTotal = orig }()

	cases := []struct {
		name  string
		out   string
		err   error
		want  string
		about string
	}{
		{name: "vm smaller than host", out: "25232719872\n", want: "12031m",
			about: "23.5 GiB VM on a 48 GiB host: half the VM, not half the host"},
		{name: "caps at sbx ceiling", out: "137438953472", want: "32768m",
			about: "128 GiB daemon would give 64 GiB; sbx caps at 32"},
		{name: "too small to bound", out: "1073741824", want: "",
			about: "a 1 GiB daemon says more about breakage than policy"},
		{name: "daemon unreachable", err: errNoDaemon, want: ""},
		{name: "unparseable", out: "not-a-number", want: ""},
		{name: "zero", out: "0", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(EnvMemory, "")
			t.Setenv(EnvInstances, "")
			sh.DockerMemTotal = func() ([]byte, error) { return []byte(c.out), c.err }
			if got := MemoryLimit(); got != c.want {
				t.Errorf("MemoryLimit()=%q, want %q (%s)", got, c.want, c.about)
			}
		})
	}
}

func TestMemoryLimitDividesByTheIntendedInstanceCount(t *testing.T) {
	orig := sh.DockerMemTotal
	defer func() { sh.DockerMemTotal = orig }()
	const daemon = "25232719872"

	for _, c := range []struct {
		name, instances, want, about string
	}{
		{"unset keeps the historical half", "", "12031m",
			"nobody who set nothing gets a behaviour change"},
		{"one sandbox", "1", "24063m", "the whole daemon when it is the only one"},
		{"two is the default", "2", "12031m", "explicit 2 and unset must agree"},
		{"four sandboxes", "4", "6015m", "4 x 6015m = 23.5 GiB, the daemon exactly"},
		{"zero is not a count", "0", "12031m", "a nonsense divisor falls back, never to infinity"},
		{"negative", "-4", "12031m", "as above"},
		{"not a number", "lots", "12031m", "a typo must not starve every sandbox"},
		{"absurd count yields to sbx", "64", "", "the share is under the floor, so sbx's own default applies"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(EnvMemory, "")
			t.Setenv(EnvInstances, c.instances)
			sh.DockerMemTotal = func() ([]byte, error) { return []byte(daemon), nil }
			if got := MemoryLimit(); got != c.want {
				t.Errorf("%s=%q: MemoryLimit()=%q, want %q (%s)", EnvInstances, c.instances, got, c.want, c.about)
			}
		})
	}
}

func TestMemoryLimitTakesAnExplicitCapOverTheDerivation(t *testing.T) {
	orig := sh.DockerMemTotal
	defer func() { sh.DockerMemTotal = orig }()

	for _, c := range []struct {
		name, mem, want, about string
		daemonErr              error
	}{
		{name: "8g, the four-instance budget", mem: "8g", want: "8192m",
			about: "4 x 8 GiB = 32 GiB on a 48 GiB host"},
		{name: "megabytes", mem: "8192m", want: "8192m", about: "same size, sbx's other unit"},
		{name: "gib spelling", mem: "8GiB", want: "8192m", about: "case and the ib suffix both accepted"},
		{name: "bare bytes", mem: "8589934592", want: "8192m", about: "a unitless value is bytes"},
		{name: "above sbx's ceiling", mem: "64g", want: "32768m", about: "clamped to the 32 GiB cap"},
		{name: "below the floor", mem: "128m", want: "",
			about: "yields to sbx rather than setting a limit no sandbox starts under"},
		{name: "malformed falls back to the derivation", mem: "eight gigs", want: "12031m",
			about: "sbx would reject it mid-run, naming a flag and not the variable behind it"},
		{name: "negative", mem: "-8g", want: "12031m", about: "as above"},
		{name: "explicit cap survives an unreachable daemon", mem: "8g", want: "8192m",
			daemonErr: errNoDaemon, about: "never consults the daemon at all"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(EnvInstances, "")
			t.Setenv(EnvMemory, c.mem)
			sh.DockerMemTotal = func() ([]byte, error) { return []byte("25232719872"), c.daemonErr }
			if got := MemoryLimit(); got != c.want {
				t.Errorf("%s=%q: MemoryLimit()=%q, want %q (%s)", EnvMemory, c.mem, got, c.want, c.about)
			}
		})
	}
}

// An empty limit must leave the argv untouched, so a daemon we cannot read falls
// back to sbx's own default rather than to a limit we invented.
func TestRunArgsMemoryIsOptional(t *testing.T) {
	with := RunArgs(RunConfig{Agent: "a", Memory: "12031m"})
	if !slices.Contains(with, "-m") || !slices.Contains(with, "12031m") {
		t.Errorf("RunArgs must pass -m when set, got %q", with)
	}
	without := RunArgs(RunConfig{Agent: "a"})
	if slices.Contains(without, "-m") {
		t.Errorf("RunArgs must omit -m when unset, got %q", without)
	}
}

// A load that reports success without landing must NOT leave a receipt: the receipt
// is what makes staleness permanent, because HasTemplate then skips the reload
// forever while the store serves the old image.
func TestLoadThatDoesNotLandLeavesNoReceipt(t *testing.T) {
	origLoad, origList := sh.TemplateLoad, sh.TemplateList
	t.Cleanup(func() { sh.TemplateLoad, sh.TemplateList = origLoad, origList })

	// The store keeps the OLD image no matter what it is handed.
	sh.TemplateList = func() ([]byte, error) {
		return []byte("REPOSITORY                  TAG      IMAGE ID       FLAVOR   CREATED\n" +
			"docker.io/proveo/cursor     latest   5fcb2266417f            10 hours ago\n"), nil
	}
	sh.TemplateLoad = func(string) error { return nil } // exits 0, changes nothing
	stubIDs(t, map[string]string{"proveo/cursor:latest": "06f0e0810f10"})
	dir := stubReceipts(t, map[string]string{})

	err := EnsureTemplate("proveo/cursor:latest", nil)
	if err == nil {
		t.Fatal("a load that did not land must be an error, not a silent success")
	}
	for _, want := range []string{"5fcb2266417f", "06f0e0810f10", "did not take"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q so the mismatch is actionable: %v", want, err)
		}
	}
	if b, statErr := os.ReadFile(filepath.Join(dir, "proveo_cursor_latest")); statErr == nil {
		t.Errorf("no receipt may be written for a load that did not land, got %q", b)
	}
}

// PROVEO_SBX_RELOAD is the escape hatch for a store that has desynced in a way
// proveo cannot see: it drops the image first, because removal is deterministic
// where overwriting is merely expected.
func TestForceReloadDropsFirst(t *testing.T) {
	origLoad, origRemove, origList := sh.TemplateLoad, sh.TemplateRemove, sh.TemplateList
	t.Cleanup(func() { sh.TemplateLoad, sh.TemplateRemove, sh.TemplateList = origLoad, origRemove, origList })

	var removed, loaded int
	sh.TemplateRemove = func(string) error { removed++; return nil }
	sh.TemplateLoad = func(string) error { loaded++; return nil }
	sh.TemplateList = func() ([]byte, error) {
		return []byte("REPOSITORY                  TAG      IMAGE ID       FLAVOR   CREATED\n" +
			"docker.io/proveo/cursor     latest   abcabcabcabc            1 minute ago\n"), nil
	}
	stubIDs(t, map[string]string{"proveo/cursor:latest": "abcabcabcabc"})
	stubReceipts(t, map[string]string{"proveo/cursor:latest": "abcabcabcabc"})

	// Without the hatch, a matching receipt short-circuits the whole thing.
	if err := EnsureTemplate("proveo/cursor:latest", nil); err != nil {
		t.Fatal(err)
	}
	if removed != 0 || loaded != 0 {
		t.Errorf("a current template must not be touched, got removed=%d loaded=%d", removed, loaded)
	}

	t.Setenv("PROVEO_SBX_RELOAD", "1")
	if err := EnsureTemplate("proveo/cursor:latest", nil); err != nil {
		t.Fatal(err)
	}
	if removed != 1 || loaded != 1 {
		t.Errorf("the hatch must drop then load, got removed=%d loaded=%d", removed, loaded)
	}

	for _, off := range []string{"", "0", "no", "maybe"} {
		if ForceReload(func(string) string { return off }) {
			t.Errorf("%q must not force a reload", off)
		}
	}
}

// The agent must be one sbx already ships: its list is closed, a Kit cannot extend
// it, and naming one of our own is what skipped the binding gate on every run.
func TestBuiltinAgentNamesOnlySbxsOwn(t *testing.T) {
	t.Parallel()
	// Exactly the names `sbx run --help` prints.
	sbxKnows := map[string]bool{
		"claude": true, "codex": true, "copilot": true, "cursor": true,
		"docker-agent": true, "droid": true, "gemini": true, "kiro": true,
		"opencode": true, "shell": true,
	}
	for _, target := range SbxTargets() {
		agent := BuiltinAgent(target)
		if !sbxKnows[agent] {
			t.Errorf("target %q maps to %q, which sbx does not ship", target, agent)
		}
		if strings.HasPrefix(agent, "proveo") {
			t.Errorf("target %q names an agent of our own (%q); that is the bug this replaces", target, agent)
		}
	}
	// A target with no sbx counterpart returns "" here — and is NOT thereby off the
	// backend. AgentFor puts it on sbx's own shell agent instead, which is what lets
	// `docker: sbx` mean "runs in a sandbox" for every harness rather than only for
	// the ones sbx happens to ship an agent for.
	if got := BuiltinAgent("cecli"); got != "" {
		t.Errorf("cecli has no built-in sbx agent; got %q", got)
	}
}

// Every harness must reach a sandbox, including the ones sbx has no name for.
// cecli is aider and nothing on the closed list is aider, so it runs under the
// shell agent carrying its own launch command. Before this, such a target
// resolved to the empty agent name and sbx read the first workspace path as an
// agent — "is not a sandbox or known agent". SPEC: _spec/_plans/retire-dind.puml
func TestAgentForSandboxesEveryTarget(t *testing.T) {
	t.Parallel()
	sbxKnows := map[string]bool{
		"claude": true, "codex": true, "copilot": true, "cursor": true,
		"docker-agent": true, "droid": true, "gemini": true, "kiro": true,
		"opencode": true, "shell": true,
	}
	for _, c := range []struct {
		target  string
		agent   string
		command []string
	}{
		{"claudecode", "claude", nil},
		{"cursor", "cursor", nil},
		{"opencode", "opencode", nil},
		{"cecli", ShellAgent, []string{"cecli"}},
		{"some-future-harness", ShellAgent, []string{"some-future-harness"}},
	} {
		agent, command := AgentFor(c.target)
		if agent != c.agent {
			t.Errorf("AgentFor(%q) agent = %q, want %q", c.target, agent, c.agent)
		}
		if !sbxKnows[agent] {
			t.Errorf("AgentFor(%q) resolved %q, which sbx does not ship", c.target, agent)
		}
		if strings.Join(command, " ") != strings.Join(c.command, " ") {
			t.Errorf("AgentFor(%q) command = %v, want %v", c.target, command, c.command)
		}
	}
	// A built-in owns its own launch: handing it a command would put our word in
	// its argv, which is the bug --shell exists to avoid.
	for _, target := range SbxTargets() {
		if _, command := AgentFor(target); command != nil {
			t.Errorf("AgentFor(%q) must leave the launch to the built-in agent, got %v", target, command)
		}
	}
	// No target, no agent — never the shell agent with an empty command, which sbx
	// would read as a workspace path.
	if agent, command := AgentFor(""); agent != "" || command != nil {
		t.Errorf(`AgentFor("") = %q, %v; want no agent at all`, agent, command)
	}
}

// A degraded daemon does not fail — it WAITS. Measured after heavy sandbox churn:
// `docker info` and `docker ps` each took over two minutes to return. MemoryLimit
// runs on every sbx launch including `--print`, so an unbounded call turns a slow
// daemon into a proveo that hangs with nothing on screen.
func TestMemoryLimitSurvivesAHangingDaemon(t *testing.T) {
	orig := sh.DockerMemTotal
	t.Cleanup(func() { sh.DockerMemTotal = orig })

	sh.DockerMemTotal = func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		// `sleep 60` stands in for the wedged daemon; the context is what must end it.
		return exec.CommandContext(ctx, "sh", "-c", "sleep 60").Output()
	}
	done := make(chan string, 1)
	go func() { done <- MemoryLimit() }()
	select {
	case got := <-done:
		// An unreadable daemon means no -m flag, which leaves sbx's own default in
		// place. That is the documented fallback, not a failure.
		if got != "" {
			t.Errorf("a daemon that never answers must yield no limit, got %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MemoryLimit did not return: the daemon call is unbounded, and every launch hangs with it")
	}
}

// The bound must be long enough that a healthy-but-loaded daemon still gets its
// answer — a limit that times out in practice silently loses the OOM protection
// MemoryLimit exists to provide.
func TestDockerInfoTimeoutIsGenerous(t *testing.T) {
	if dockerInfoTimeout < 5*time.Second {
		t.Errorf("dockerInfoTimeout = %s, too tight for a loaded daemon", dockerInfoTimeout)
	}
}

// --clone is creation-time only, so it has to reach the argv of the run that
// CREATES the sandbox — there is no later toggle. It also has to sit among the
// flags, before the agent positional, or sbx reads it as a workspace path.
func TestRunArgsCarriesClone(t *testing.T) {
	t.Parallel()
	got := RunArgs(RunConfig{Name: "s", Image: "img", Agent: "claude", Clone: true,
		Mounts: []Mount{{Host: "/w", Container: "/w"}}})
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--clone") {
		t.Fatalf("--clone missing from %v", got)
	}
	flag, agent := indexOf(got, "--clone"), indexOf(got, "claude")
	if flag < 0 || agent < 0 || flag > agent {
		t.Errorf("--clone must precede the agent positional, got %v", got)
	}
}

// Absent by default: a run that did not ask for a clone must edit the mounted
// checkout, which is what every existing caller expects.
func TestRunArgsOmitsCloneByDefault(t *testing.T) {
	t.Parallel()
	got := RunArgs(RunConfig{Name: "s", Image: "img", Agent: "claude"})
	if strings.Contains(strings.Join(got, " "), "--clone") {
		t.Errorf("--clone leaked into a non-clone run: %v", got)
	}
}

func indexOf(xs []string, want string) int {
	for i, x := range xs {
		if x == want {
			return i
		}
	}
	return -1
}

// `sbx exec` on a STOPPED sandbox starts it, which re-runs the kit's startup seed.
// A copy-out issued against one is therefore not reading a quiescent home, and
// everything that lands in the operator's home afterwards postdates the run — so
// "exists" and "running" cannot be the same question. Reading a stopped sandbox as
// running is how a restart's own artifacts passed as the run's evidence.
func TestRunningSeparatesAStoppedSandboxFromALiveOne(t *testing.T) {
	orig := sh.SandboxList
	t.Cleanup(func() { sh.SandboxList = orig })

	// The real shape, workspace column and all: STATUS is the third field, and the
	// header's first field is the literal SANDBOX so it can never match a name.
	sh.SandboxList = func() ([]byte, error) {
		return []byte("SANDBOX                   AGENT    STATUS    PORTS   WORKSPACE\n" +
			"proveo-1787-live          claude   running           /w/repo, /w/repo/reports\n" +
			"proveo-1787-dead          claude   stopped           /w/repo\n"), nil
	}
	if !Running("proveo-1787-live") {
		t.Error("a running sandbox must read as running")
	}
	if Running("proveo-1787-dead") {
		t.Error("a stopped sandbox read as running; a copy-out against it races its own restore")
	}
	if Running("proveo-1787-absent") || Running("") {
		t.Error("a sandbox not in the listing is not running")
	}
	if Running("SANDBOX") {
		t.Error("the header row was matched as a sandbox")
	}

	// Unreadable listing: the pessimistic answer. Callers use this to decide whether
	// to trust a copy-out, and guessing "running" is the guess that lets a restart's
	// leftovers be reported as what the agent said.
	sh.SandboxList = func() ([]byte, error) { return nil, errors.New("daemon down") }
	if Running("proveo-1787-live") {
		t.Error("an unreadable listing must not be read as a live sandbox")
	}
}

// The store is GLOBAL and outlives every run, so proveo cannot tell from its own
// decision whether the agent got a credential. Names are all that can be read back
// — the value is never printed — and reading them is the difference between naming
// the real cause and blaming the credential for a run that was authenticated.
func TestStoredSecretNamesReadsNamesAndSkipsTheHeader(t *testing.T) {
	orig := sh.SecretList
	t.Cleanup(func() { sh.SecretList = orig })

	sh.SecretList = func() ([]byte, error) {
		return []byte("SCOPE      TYPE      NAME                      SECRET\n" +
			"(global)   service   ANTHROPIC_API_KEY         (stored)\n" +
			"(global)   service   CLAUDE_CODE_OAUTH_TOKEN   (stored)\n" +
			"(global)   service   anthropic                 (oauth configured)\n"), nil
	}
	got := StoredSecretNames()
	want := []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "anthropic"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("row %d: want %q, got %q", i, w, got[i])
		}
	}

	// No store, or no sbx: silence. An empty list means "proveo learned nothing",
	// and a caller must not read that as "the store is empty" — which is why the
	// hint this feeds never claims a stored value is missing, only unreadable.
	sh.SecretList = func() ([]byte, error) { return nil, errors.New("no daemon") }
	if got := StoredSecretNames(); got != nil {
		t.Errorf("an unreadable store must yield no names, got %v", got)
	}
}

func TestPolicyEvidenceArgsMatchTheCLI(t *testing.T) {
	t.Parallel()
	if got, want := strings.Join(PolicyLogArgs("proveo-1"), " "), "policy log proveo-1 --json"; got != want {
		t.Errorf("PolicyLogArgs = %q, want %q", got, want)
	}
	if got, want := strings.Join(CheckNetworkArgs("api.example.com"), " "), "policy check network --json api.example.com"; got != want {
		t.Errorf("CheckNetworkArgs = %q, want %q", got, want)
	}
}

func TestNetworkAllowedReadsTheDaemonsAnswer(t *testing.T) {
	orig := sh.PolicyCheck
	t.Cleanup(func() { sh.PolicyCheck = orig })

	sh.PolicyCheck = func(string) ([]byte, error) {
		return []byte(`{"action":"net:connect:tcp","allowed":true,"context":"global"}`), nil
	}
	if allowed, known := NetworkAllowed("evil.example.invalid"); !allowed || !known {
		t.Errorf("allow-all baseline: allowed=%v known=%v, want true/true", allowed, known)
	}

	sh.PolicyCheck = func(string) ([]byte, error) {
		return []byte(`{"action":"net:connect:tcp","allowed":false,"context":"global"}`), nil
	}
	if allowed, known := NetworkAllowed("evil.example.invalid"); allowed || !known {
		t.Errorf("deny baseline: allowed=%v known=%v, want false/true", allowed, known)
	}

	sh.PolicyCheck = func(string) ([]byte, error) { return nil, errors.New("sandboxd unreachable") }
	if allowed, known := NetworkAllowed("evil.example.invalid"); allowed || known {
		t.Errorf("unreadable: allowed=%v known=%v, want false/false", allowed, known)
	}
}

func TestCPULimitSharesTheHostOnlyWhenAskedTo(t *testing.T) {
	orig := numCPU
	defer func() { numCPU = orig }()
	numCPU = func() int { return 14 }

	for _, c := range []struct {
		name, cpus, instances string
		want                  int
		about                 string
	}{
		{"both unset", "", "", 0, "sbx keeps every host CPU; setting nothing changes nothing"},
		{"one instance", "", "1", 0, "a lone sandbox is not sharing with anyone"},
		{"four instances", "", "4", 7, "half the machine, not a quarter: an idle vCPU costs nothing"},
		{"two instances", "", "2", 7, "the burst share does not vary with n"},
		{"explicit cpus wins", "6", "4", 6, "a measured answer beats the derivation"},
		{"explicit above the host", "64", "", 14, "clamped to what the host has"},
		{"more instances than cores", "", "32", 7, "still half: CPU sums past the host on purpose"},
		{"garbage instances", "", "many", 0, "a typo leaves sbx's default alone"},
		{"garbage cpus falls back", "lots", "4", 7, "as above, then the derivation applies"},
		{"zero cpus", "0", "", 0, "not a count; sbx's default stands"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(EnvCPUs, c.cpus)
			t.Setenv(EnvInstances, c.instances)
			if got := CPULimit(); got != c.want {
				t.Errorf("%s=%q %s=%q: CPULimit()=%d, want %d (%s)",
					EnvCPUs, c.cpus, EnvInstances, c.instances, got, c.want, c.about)
			}
		})
	}
}

func TestRunArgsCPUsIsOptional(t *testing.T) {
	t.Parallel()
	with := RunArgs(RunConfig{Agent: "a", CPUs: 3})
	if i := indexOf(with, "--cpus"); i < 0 || with[i+1] != "3" {
		t.Errorf("--cpus 3 missing from %q", with)
	}
	if got := RunArgs(RunConfig{Agent: "a"}); slices.Contains(got, "--cpus") {
		t.Errorf("an unset limit must leave the argv alone, got %q", got)
	}
}

func TestCreateArgsIsRunArgsWithoutAttaching(t *testing.T) {
	t.Parallel()
	cfg := RunConfig{
		Name: "proveo-capacity-1", Agent: "shell", Image: "img",
		Memory: "8192m", CPUs: 7,
		Mounts:  []Mount{{Host: "/w"}},
		Command: []string{"sleep", "600"},
	}
	got := CreateArgs(cfg)
	want := []string{"create", "--name", "proveo-capacity-1", "-t", "img", "-m", "8192m", "--cpus", "7", "shell", "/w"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CreateArgs()=\n%q\nwant\n%q", got, want)
	}
	if slices.Contains(got, "--") {
		t.Error("create takes no trailing agent command; a `--` would be read as a workspace")
	}
	if RunArgs(cfg)[0] != "run" {
		t.Error("CreateArgs mutated the shared config and broke RunArgs")
	}
}

// AuthLoginArgs is sbx's own documented producer for its oauth slot; nothing
// else populates it, so the argv has to match what sbx accepts.
func TestAuthLoginArgs(t *testing.T) {
	t.Parallel()
	got := AuthLoginArgs("claude", "/w")
	want := []string{"run", "claude", "/w", "--", "auth", "login"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("AuthLoginArgs = %v, want %v", got, want)
	}
	// An agent with no known sign-in, and the two empty shapes, render nothing
	// rather than a command that would fail in front of the operator.
	for _, tc := range [][2]string{{"cursor", "/w"}, {"", "/w"}, {"claude", ""}} {
		if a := AuthLoginArgs(tc[0], tc[1]); a != nil {
			t.Errorf("AuthLoginArgs(%q, %q) = %v, want nil", tc[0], tc[1], a)
		}
	}
}
