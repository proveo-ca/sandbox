package sbx

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// withProbes pins the availability seams for one test.
func withProbes(t *testing.T, path, wantGoos, wantGoarch string, kvmMissing bool) {
	t.Helper()
	oldLook, oldGoos, oldGoarch, oldKvm, oldStat := lookPath, goos, goarch, kvmDevice, stat
	// Available now gates on the CLI's version, so the version probe has to be
	// stubbed too or these cases would shell out to whatever sbx the host holds —
	// and "darwin arm64 is supported" would start depending on a real install.
	oldVer := runVer
	runVer = func() ([]byte, error) { return []byte("sbx version: v" + MinVersion + "\n"), nil }
	t.Cleanup(func() { runVer = oldVer })
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

func TestRunArgsFull(t *testing.T) {
	got := RunArgs(RunConfig{
		Name:   "proveo-1-2",
		KitDir: "/state/egress/x/kit",
		Image:  "proveo/claudecode:latest",
		Mounts: []Mount{
			{Host: "/repo", Container: "/workspace/input"},
			{Host: "/out", Container: "/workspace/output", ReadOnly: true},
		},
		Env:     []string{"PROVEO_EVIDENCE=concise"},
		Workdir: "/workspace/input",
		Command: []string{"claude"},
	})
	want := []string{
		"run", "--name", "proveo-1-2", "--kit", "/state/egress/x/kit",
		"-v", "/repo:/workspace/input", "-v", "/out:/workspace/output:ro",
		"-e", "PROVEO_EVIDENCE=concise", "-w", "/workspace/input",
		"proveo/claudecode:latest", "--", "claude",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RunArgs()=\n%q\nwant\n%q", got, want)
	}
}

func TestRunArgsMinimal(t *testing.T) {
	got := RunArgs(RunConfig{})
	want := []string{"run"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RunArgs()=%q, want %q", got, want)
	}
}

func TestRemoveAndSecretSetArgs(t *testing.T) {
	if got, want := RemoveArgs("s1"), []string{"rm", "s1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RemoveArgs=%q want %q", got, want)
	}
	if got, want := SecretSetArgs("CLAUDE_CODE_OAUTH_TOKEN"), []string{"secret", "set", "CLAUDE_CODE_OAUTH_TOKEN"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SecretSetArgs=%q want %q", got, want)
	}
}

func TestSecretSetPipesValueViaStdin(t *testing.T) {
	var gotName, gotValue string
	old := secretSet
	secretSet = func(name, value string) error {
		gotName, gotValue = name, value
		return nil
	}
	t.Cleanup(func() { secretSet = old })
	if err := SecretSet("TOK", "sekret"); err != nil {
		t.Fatal(err)
	}
	if gotName != "TOK" || gotValue != "sekret" {
		t.Fatalf("SecretSet delivered name=%q value=%q", gotName, gotValue)
	}
}

func TestWriteKitRendersDenyByDefaultAllowlist(t *testing.T) {
	dir := t.TempDir()
	kitDir, err := WriteKit(filepath.Join(dir, "kit"), Kit{
		Name:           "claudecode",
		Image:          "proveo/claudecode:latest",
		Network:        KitNet{AllowedDomains: []string{"api.anthropic.com", "statsig.anthropic.com"}},
		CredentialsEnv: []string{"CLAUDE_CODE_OAUTH_TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if kitDir != filepath.Join(dir, "kit") {
		t.Fatalf("WriteKit returned %q, want the kit directory (what --kit takes)", kitDir)
	}
	path := filepath.Join(kitDir, "spec.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"name: claudecode",
		"image: proveo/claudecode:latest",
		"allowedDomains:",
		"- api.anthropic.com",
		"- statsig.anthropic.com",
		"credentialsEnv:",
		"- CLAUDE_CODE_OAUTH_TOKEN",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("spec.yaml missing %q:\n%s", want, s)
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("spec.yaml perms %v, want 0600 (contains no secrets but posture is private)", fi.Mode().Perm())
	}
}

func TestWriteKitOmitsEmptyNetwork(t *testing.T) {
	dir := t.TempDir()
	kitDir, err := WriteKit(dir, Kit{Name: "cursor", Image: "proveo/cursor:latest"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(kitDir, "spec.yaml"))
	if strings.Contains(string(b), "allowedDomains") {
		t.Errorf("empty allowlist should be omitted:\n%s", b)
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
	orig := runVer
	t.Cleanup(func() { runVer = orig })

	for _, tc := range []struct{ out, want string }{
		{"sbx version: v0.39.0 def8cb0523a77e757bdd6ef52b459fe374f3783e\n", "0.39.0"},
		{"sbx version: v0.35.0\n", "0.35.0"},
		{"0.40.1\n", "0.40.1"},
	} {
		runVer = func() ([]byte, error) { return []byte(tc.out), nil }
		got, err := Version()
		if err != nil || got != tc.want {
			t.Errorf("Version() from %q = %q, %v; want %q", tc.out, got, err, tc.want)
		}
	}

	runVer = func() ([]byte, error) { return []byte("nothing here"), nil }
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
	origOS, origArch, origLook, origVer := goos, goarch, lookPath, runVer
	t.Cleanup(func() { goos, goarch, lookPath, runVer = origOS, origArch, origLook, origVer })

	goos, goarch = "darwin", "arm64"
	lookPath = func(string) (string, error) { return "/usr/local/bin/sbx", nil }

	runVer = func() ([]byte, error) { return []byte("sbx version: v0.35.0\n"), nil }
	ok, why := Available()
	if ok {
		t.Error("a CLI older than MinVersion must not be selected")
	}
	if !strings.Contains(why, MinVersion) {
		t.Errorf("the reason must name the version proveo targets, got %q", why)
	}

	runVer = func() ([]byte, error) { return []byte("sbx version: v" + MinVersion + "\n"), nil }
	if ok, why := Available(); !ok {
		t.Errorf("MinVersion exactly must be accepted, got %q", why)
	}
}

// The sandbox runtime's image store is separate from the host engine's, so a run
// has to hand the image over. It must also NOT hand it over twice: these images
// are multi-GB and `docker save | sbx template load` is the slowest step in a
// sandbox run by a wide margin.
func TestEnsureTemplateSkipsAnImageAlreadyLoaded(t *testing.T) {
	origList, origLoad := templateList, templateLoad
	t.Cleanup(func() { templateList, templateLoad = origList, origLoad })

	loads := 0
	templateLoad = func(string) error { loads++; return nil }

	templateList = func() ([]byte, error) {
		return []byte("REPOSITORY              TAG\nproveo/claudecode       latest\n"), nil
	}
	if err := EnsureTemplate("proveo/claudecode:latest", nil); err != nil {
		t.Fatalf("EnsureTemplate: %v", err)
	}
	if loads != 0 {
		t.Errorf("an image already in the store was loaded again (%d times)", loads)
	}

	templateList = func() ([]byte, error) { return []byte("REPOSITORY  TAG\n"), nil }
	if err := EnsureTemplate("proveo/claudecode:latest", nil); err != nil {
		t.Fatalf("EnsureTemplate: %v", err)
	}
	if loads != 1 {
		t.Errorf("a missing image was loaded %d times, want exactly 1", loads)
	}

	// An unreadable store is treated as "not present": loading again is wasteful,
	// but running against an image that is genuinely absent fails the whole run.
	templateList = func() ([]byte, error) { return nil, errors.New("daemon down") }
	if HasTemplate("proveo/claudecode:latest") {
		t.Error("an unreadable store must not report the image as present")
	}
}

// An empty image is not an error to load — it is nothing to load.
func TestEnsureTemplateIgnoresAnEmptyImage(t *testing.T) {
	origLoad := templateLoad
	t.Cleanup(func() { templateLoad = origLoad })
	templateLoad = func(string) error { t.Fatal("empty image must not reach the loader"); return nil }
	if err := EnsureTemplate("", nil); err != nil {
		t.Errorf("EnsureTemplate(\"\") = %v, want nil", err)
	}
}
