package sbx

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// withProbes pins the availability seams for one test: path=="" simulates a
// missing CLI; kvmMissing simulates the absence of /dev/kvm on linux.
func withProbes(t *testing.T, path, wantGoos, wantGoarch string, kvmMissing bool) {
	t.Helper()
	oldLook, oldGoos, oldGoarch, oldKvm, oldStat := lookPath, goos, goarch, kvmDevice, stat
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
	path, err := WriteKit(filepath.Join(dir, "kit"), Kit{
		Name:           "claudecode",
		Image:          "proveo/claudecode:latest",
		Network:        KitNet{AllowedDomains: []string{"api.anthropic.com", "statsig.anthropic.com"}},
		CredentialsEnv: []string{"CLAUDE_CODE_OAUTH_TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "spec.yaml" {
		t.Fatalf("unexpected kit path %q", path)
	}
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
	path, err := WriteKit(dir, Kit{Name: "cursor", Image: "proveo/cursor:latest"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "allowedDomains") {
		t.Errorf("empty allowlist should be omitted:\n%s", b)
	}
}
