package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestInitWritesNoEnvFileIntoTheWorkspace(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant")
	t.Setenv("CURSOR_API_KEY", "sk-cursor")

	// --print is the only mode that touches neither the network nor the host,
	// so it is the mode a unit test may run. What it must not do is write.
	if err := doInit(initOptions{printOnly: true, prefix: filepath.Join(dir, "prefix")}); err != nil {
		// A platform with no build is a legitimate refusal, not a failure of
		// this assertion — the directory check below still has to hold.
		if !strings.Contains(err.Error(), "no Docker Sandboxes build") &&
			!strings.Contains(err.Error(), "Apple silicon") {
			t.Fatalf("doInit(--print) = %v", err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == ".env" || strings.HasSuffix(e.Name(), ".env") {
			t.Fatalf("init created %s; provider keys in the workspace are what the run path warns about", e.Name())
		}
		if e.Name() == "prefix" {
			t.Fatalf("--print created the install prefix; it must change nothing")
		}
	}
}

// --print must not need a home directory, a network, or a writable prefix. It
// is the mode an operator reaches for on a host they have not committed to yet.
func TestInitPrintDescribesThePlanForThisHost(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	host := sbx.DetectHost()
	if _, err := sbx.PlanFor(host, filepath.Join(dir, "p"), dir); err != nil {
		t.Skipf("no sbx build for %s/%s, so there is no plan to print", host.OS, host.Arch)
	}
	if err := doInit(initOptions{printOnly: true, prefix: filepath.Join(dir, "p")}); err != nil {
		t.Fatalf("doInit(--print) = %v", err)
	}
}

// The prefix is the operator's to choose, and init must not silently install
// somewhere else. Passing one also keeps the test off $HOME.
func TestInitHonoursAnExplicitPrefix(t *testing.T) {
	t.Parallel()
	host := sbx.Host{OS: "linux", Arch: "amd64", Distro: "rocky", Like: "rhel"}
	plan, err := sbx.PlanFor(host, "/opt/sbx", "/tmp/dl")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Prefix != "/opt/sbx" {
		t.Fatalf("Prefix = %q, want the prefix given", plan.Prefix)
	}
	if plan.Bin != "/opt/sbx/bin/sbx" {
		t.Fatalf("Bin = %q, want it under the given prefix", plan.Bin)
	}
	for _, s := range plan.Steps {
		for _, e := range s.Env {
			if strings.HasPrefix(e, "PREFIX=") && e != "PREFIX=/opt/sbx" {
				t.Errorf("step %q passes %q, which is not the prefix asked for", s.What, e)
			}
		}
	}
}

func TestResolveBinPrefersWhatWasJustInstalled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", "sbx")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := resolveBin(sbx.Plan{Bin: bin}); got != sbx.Binary {
		t.Errorf("resolveBin with nothing installed = %q, want the PATH lookup %q", got, sbx.Binary)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveBin(sbx.Plan{Bin: bin}); got != bin {
		t.Errorf("resolveBin = %q, want the freshly installed %q", got, bin)
	}
	if got := resolveBin(sbx.Plan{}); got != sbx.Binary {
		t.Errorf("a plan with no Bin (the MSI) = %q, want %q", got, sbx.Binary)
	}
}

func TestDescribeHostNamesTheDistroWhenThereIsOne(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		host sbx.Host
		want string
	}{
		{sbx.Host{OS: "darwin", Arch: "arm64"}, "darwin/arm64"},
		{sbx.Host{OS: "linux", Arch: "amd64", Distro: "rocky", Version: "9.4"}, "linux/amd64 · rocky 9.4"},
		{sbx.Host{OS: "linux", Arch: "arm64", Distro: "void"}, "linux/arm64 · void"},
	} {
		if got := describeHost(tc.host); got != tc.want {
			t.Errorf("describeHost(%+v) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestInstalledVersionAsksThePrefixBeforePATH(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", "sbx")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := sbx.Plan{Bin: bin, Prefix: dir}

	if v, why := installedVersion(plan); v != "" || why == "" {
		t.Errorf("empty prefix reported version %q / %q, want a reason to install", v, why)
	}

	// A stub that answers `sbx version` the way the real one does.
	script := "#!/bin/sh\necho 'sbx version: v0.42.0 ca4a4bd'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	v, why := installedVersion(plan)
	if v != "0.42.0" {
		t.Fatalf("installedVersion = %q (%s), want the prefix's 0.42.0", v, why)
	}
	if sbx.Older(v, sbx.Release) {
		t.Fatalf("%s read from the prefix is older than the pin, so init would reinstall it every run", v)
	}
}

func TestPrefixWritableRefusesBeforeAnythingIsDownloaded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if err := prefixWritable(filepath.Join(dir, "not", "there", "yet")); err != nil {
		t.Errorf("a missing prefix under a writable parent must be allowed: %v", err)
	}
	if err := prefixWritable(""); err != nil {
		t.Errorf("an installer that owns placement has no prefix to check: %v", err)
	}

	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can write anywhere")
	}
	err := prefixWritable(filepath.Join(locked, "sbx"))
	if err == nil {
		t.Fatal("an unwritable prefix must be refused before the download")
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Errorf("the refusal must say how to proceed, got %q", err)
	}

	// The probe must leave nothing behind in a directory it could write.
	if err := prefixWritable(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".proveo-write-probe") {
			t.Errorf("the write probe left %s behind", e.Name())
		}
	}
}
