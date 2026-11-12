// SPEC: _spec/internal/shell/setup-path.puml
package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/promptkit"
	"github.com/proveo-ca/proveo/internal/results"
	"github.com/proveo-ca/proveo/internal/schedule"
	"github.com/proveo-ca/proveo/internal/shell"
)

func TestStripInstallAndSetupPathBlocks(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, ".bashrc")
	bin := filepath.Join(dir, "install", "bin")
	content := strings.Join([]string{
		"export FOO=1",
		installPathMarkerStart,
		`export PATH="` + bin + `:$PATH"`,
		installPathMarkerEnd,
		shell.Marker,
		`export PATH="` + bin + `:$PATH"`,
		"export BAR=2",
		"",
	}, "\n")
	if err := os.WriteFile(rc, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := stripPathLines(rc, bin); err != nil || !changed {
		t.Fatalf("stripPathLines = %v, %v; want a change", changed, err)
	}
	got, _ := os.ReadFile(rc)
	s := string(got)
	if strings.Contains(s, installPathMarkerStart) || strings.Contains(s, shell.Marker) || strings.Contains(s, bin) {
		t.Fatalf("markers/path not stripped:\n%s", s)
	}
	if !strings.Contains(s, "export FOO=1") || !strings.Contains(s, "export BAR=2") {
		t.Fatalf("unrelated lines lost:\n%s", s)
	}
	if changed, err := stripPathLines(rc, bin); err != nil || changed {
		t.Errorf("a second stripPathLines = %v, %v; want no change", changed, err)
	}
	if changed, err := stripPathLines(filepath.Join(dir, ".zshrc"), bin); err != nil || changed {
		t.Errorf("a missing rc = %v, %v; want no change and no error", changed, err)
	}
}

func TestUninstallLeavesAnotherDirsSetupBlock(t *testing.T) {
	home, root := uninstallHost(t)
	captureUI(t)
	other := shell.Shell{Name: "zsh"}.Block(filepath.Join(home, "go", "bin"))
	mine := shell.Shell{Name: "zsh"}.Block(filepath.Join(root, "bin"))
	rc := filepath.Join(home, ".zshrc")
	writeFile(t, rc, "export B=2\n"+other)
	writeFile(t, filepath.Join(home, ".bashrc"), "export A=1\n")
	if f := findInstalled(root, shellRCCandidates(home), promptKitOptions(false)); f.path {
		t.Errorf("findInstalled = %+v; another dir's setup block is not this root's PATH line", f)
	}
	writeFile(t, rc, "export B=2\n"+other+mine)
	if err := uninstall(true, false, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(rc); !strings.Contains(string(b), other) || strings.Contains(string(b), root) {
		t.Errorf("uninstall must strip this root's setup block and keep ~/go/bin's:\n%s", b)
	}
}

func TestUninstallLeavesAnotherRootsInstallBlock(t *testing.T) {
	home, root := uninstallHost(t)
	captureUI(t)
	block := func(bin string) string {
		return installPathMarkerStart + "\nexport PATH=\"" + bin + ":$PATH\"\n" + installPathMarkerEnd + "\n"
	}
	other := block(filepath.Join(home, "other-root", "bin"))
	rc := filepath.Join(home, ".bashrc")
	writeFile(t, rc, "export A=1\n"+other)
	writeFile(t, filepath.Join(home, ".zshrc"), "export B=2\n")
	if f := findInstalled(root, shellRCCandidates(home), promptKitOptions(false)); f.path {
		t.Errorf("findInstalled = %+v; another root's install.sh block is not this root's PATH line", f)
	}
	writeFile(t, rc, "export A=1\n"+other+block(filepath.Join(root, "bin"))+installPathMarkerStart+"\n"+installPathMarkerEnd+"\n")
	if err := uninstall(true, false, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(rc); string(b) != "export A=1\n"+other {
		t.Errorf("uninstall must strip this root's block and an empty one, and keep the other root's:\n%s", b)
	}
}

func TestRemoveInstallRootSafety(t *testing.T) {
	isolatedHost(t)
	if err := removeInstallRoot("/"); err == nil {
		t.Fatal("must refuse /")
	}
	home, _ := os.UserHomeDir()
	if err := removeInstallRoot(home); err == nil {
		t.Fatal("must refuse $HOME")
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("the refused $HOME is gone: %v", err)
	}
	root := filepath.Join(t.TempDir(), "proveo-root")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeInstallRoot(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("root still exists: %v", err)
	}
}

func TestDoUninstallYes(t *testing.T) {
	home, root := uninstallHost(t)
	captureUI(t)
	if err := doUninstall(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("install root not removed")
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".bashrc")); strings.Contains(string(b), root) {
		t.Errorf("install.sh's PATH block stayed: %q", b)
	}
}

// uninstallHost is isolatedHost with a CDN install under the temp HOME: the root's bin/proveo is the running
// binary, ~/.bashrc holds install.sh's PATH block and ~/.zshrc setup's; it returns HOME and the root.
func uninstallHost(t *testing.T) (home, root string) {
	t.Helper()
	isolatedHost(t)
	home, root = os.Getenv("HOME"), os.Getenv("PROVEO_INSTALL_ROOT")
	t.Setenv("SHELL", "/bin/zsh")
	bin := filepath.Join(root, "bin")
	writeFile(t, filepath.Join(bin, "proveo"), "x")
	runningAs(t, filepath.Join(bin, "proveo"))
	writeFile(t, filepath.Join(home, ".bashrc"), "export A=1\n"+installPathMarkerStart+"\nexport PATH=\""+bin+":$PATH\"\n"+installPathMarkerEnd+"\n")
	writeFile(t, filepath.Join(home, ".zshrc"), "export B=2\n"+shell.Shell{Name: "zsh"}.Block(bin))
	return home, root
}

// runningAs points the running binary's path at exe for the test.
func runningAs(t *testing.T, exe string) {
	t.Helper()
	prev := executable
	t.Cleanup(func() { executable = prev })
	executable = func() (string, error) { return exe, nil }
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stubbed puts logging sbx, launchctl and systemctl first on PATH and returns the file each call lands in.
func stubbed(t *testing.T) (calls string) {
	t.Helper()
	bin := t.TempDir()
	calls = filepath.Join(t.TempDir(), "calls")
	for _, name := range []string{"sbx", "launchctl", "systemctl"} {
		stub := "#!/bin/sh\nprintf '%s %s\\n' " + name + " \"$*\" >> " + strconv.Quote(calls) + "\nexit 1\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func TestUninstallPrintsWhatLeftAsOneBlockAndClosesOnUninstalled(t *testing.T) {
	home, root := uninstallHost(t)
	out := captureUI(t)
	if err := uninstall(true, false, nil); err != nil {
		t.Fatal(err)
	}
	want := "root: ~/.proveo\n" +
		"path: ~/.zshrc\n" +
		"      ~/.bashrc\n" +
		"ok: proveo — uninstalled\n"
	if out.String() != want {
		t.Errorf("uninstall printed\n%s\nwant\n%s", out, want)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Error("the install root stayed")
	}
	for rc, keep := range map[string]string{".bashrc": "export A=1\n", ".zshrc": "export B=2\n\n"} {
		if b, _ := os.ReadFile(filepath.Join(home, rc)); string(b) != keep {
			t.Errorf("%s = %q, want %q", rc, b, keep)
		}
	}
}

func TestUninstallWithTheBinaryOutsideTheRootEndsOnWhatItLeft(t *testing.T) {
	home, _ := uninstallHost(t)
	outside := filepath.Join(home, "go", "bin", "proveo")
	writeFile(t, outside, "x")
	runningAs(t, outside)
	out := captureUI(t)
	if err := uninstall(true, false, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), "warn: ~/go/bin/proveo — not removed\n    outside ~/.proveo\nwarn: proveo — uninstalled; 1 left\n") {
		t.Errorf("uninstall did not end on the binary it left:\n%s", out)
	}
	if strings.Contains(out.String(), "hash -r") || strings.Contains(out.String(), "ok: ") {
		t.Errorf("uninstall printed a how-to or a ✓ although a proveo stays:\n%s", out)
	}

	if err := os.Chmod(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(outside)+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeFile(t, filepath.Join(os.Getenv("PROVEO_INSTALL_ROOT"), "bin", "proveo"), "x")
	out.Reset()
	if err := uninstall(true, false, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), "warn: ~/go/bin/proveo — still on PATH\nwarn: proveo — uninstalled; 1 left\n") {
		t.Errorf("a proveo this shell's PATH finds is not the one ⚠:\n%s", out)
	}
}

func TestUninstallSaysWhatStaysFromItsOwnRecordsAndAsksNoServiceManager(t *testing.T) {
	uninstallHost(t)
	calls := stubbed(t)
	files := schedule.TickFiles()
	if len(files) == 0 {
		t.Skipf("proveo registers no tick on %s", runtime.GOOS)
	}
	for _, f := range files {
		writeFile(t, f, "tick")
	}
	if err := results.Put(results.Dir(os.Getenv("PROVEO_STATE_ROOT")), results.Entry{Kind: results.Kept, Key: "proveo-claudecode-1234", Why: "kept for diagnosis"}); err != nil {
		t.Fatal(err)
	}
	out := captureUI(t)
	if err := uninstall(true, false, nil); err != nil {
		t.Fatal(err)
	}
	fix := "launchctl bootout gui/"
	if runtime.GOOS == "linux" {
		fix = "systemctl --user disable --now proveo-schedule.timer; rm ~/.config/systemd/user/proveo-schedule.service"
	}
	got := out.String()
	_, tail, _ := strings.Cut(got, "warn: schedule tick — still registered\n    "+fix)
	if !strings.HasSuffix(tail, "\nwarn: proveo-claudecode-1234 — kept, its output only inside\nwarn: proveo — uninstalled; 2 left\n") {
		t.Errorf("uninstall did not name the tick with its fix, then the kept sandbox with none:\n%s", got)
	}
	if strings.Contains(got, "sbx rm") {
		t.Errorf("uninstall named a command that deletes a kept sandbox:\n%s", got)
	}
	if b, err := os.ReadFile(calls); err == nil {
		t.Errorf("uninstall asked a service manager or sbx: %s", b)
	}
}

func TestUninstallAsksNamingEachPartItFound(t *testing.T) {
	for _, c := range []struct {
		found installFound
		want  string
	}{
		{installFound{at: "/r", root: true, path: true, kit: true}, "remove /r, its PATH lines and the prompt kit?"},
		{installFound{at: "/r", root: true, path: true}, "remove /r and its PATH lines?"},
		{installFound{at: "/r", root: true, kit: true}, "remove /r and the prompt kit?"},
		{installFound{at: "/r", root: true}, "remove /r?"},
		{installFound{at: "/r", path: true, kit: true}, "remove proveo's PATH lines and the prompt kit?"},
		{installFound{at: "/r", kit: true}, "remove the prompt kit?"},
		{installFound{at: "/r"}, ""},
	} {
		if got := c.found.question(); got != c.want {
			t.Errorf("%+v asks %q, want %q", c.found, got, c.want)
		}
	}

	home, root := uninstallHost(t)
	writeFile(t, filepath.Join(home, ".config", "starship.toml"), promptkit.WithSegment(""))
	out := captureUI(t)
	if err := uninstall(false, true, strings.NewReader("n\n")); err != nil {
		t.Fatal(err)
	}
	if want := "remove ~/.proveo, its PATH lines and the prompt kit? [y/N] warn: uninstall — cancelled\n"; out.String() != want {
		t.Errorf("a declined uninstall printed\n%q\nwant\n%q", out, want)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("a declined uninstall removed the root: %v", err)
	}

	out.Reset()
	if err := uninstall(false, true, strings.NewReader("y\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "remove ~/.proveo, its PATH lines and the prompt kit? [y/N] root:   ~/.proveo\n") ||
		!strings.Contains(out.String(), "prompt: ~/.config/starship.toml\n") || !strings.HasSuffix(out.String(), "ok: proveo — uninstalled\n") {
		t.Errorf("a confirmed uninstall printed\n%s", out)
	}

	out.Reset()
	if err := uninstall(false, true, strings.NewReader("")); err != nil || out.String() != "ok: proveo — nothing to remove\n" {
		t.Errorf("with nothing left it asked or said %q, %v", out, err)
	}
}

func TestUninstallWithoutATerminalOrYesFailsWithOneHowTo(t *testing.T) {
	_, root := uninstallHost(t)
	out := captureUI(t)
	err := uninstall(false, false, nil)
	var rep backend.Reported
	if !errors.As(err, &rep) {
		t.Fatalf("err = %v, want Reported", err)
	}
	if want := "error: proveo — not uninstalled\n    `proveo uninstall --yes` uninstalls it without asking\n"; out.String() != want {
		t.Errorf("printed %q, want %q", out, want)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("the refusal removed the root: %v", err)
	}
}

func TestARemovalThatFailedIsAWarningNotAFact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only file")
	}
	home, _ := uninstallHost(t)
	rc := filepath.Join(home, ".bashrc")
	if err := os.Chmod(rc, 0o444); err != nil {
		t.Fatal(err)
	}
	out := captureUI(t)
	if err := uninstall(true, false, nil); err != nil {
		t.Fatal(err)
	}
	want := "root: ~/.proveo\n" +
		"path: ~/.zshrc\n" +
		"warn: ~/.bashrc — PATH line not removed\n" +
		"    open ~/.bashrc: permission denied\n" +
		"warn: proveo — uninstalled; 1 left\n"
	if out.String() != want {
		t.Errorf("uninstall printed\n%s\nwant\n%s", out, want)
	}
}

func TestUninstallClosesOnTheRootItCouldNotRemove(t *testing.T) {
	home, _ := uninstallHost(t)
	t.Setenv("PROVEO_INSTALL_ROOT", home)
	out := captureUI(t)
	err := uninstall(true, false, nil)
	var rep backend.Reported
	if !errors.As(err, &rep) || !strings.HasSuffix(out.String(), "error: ~ — not removed\n    an unsafe install root\n") {
		t.Errorf("an unsafe root ended on %v:\n%s", err, out)
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("the refused home is gone: %v", err)
	}

	if os.Geteuid() == 0 {
		return
	}
	root := filepath.Join(home, ".proveo")
	t.Setenv("PROVEO_INSTALL_ROOT", root)
	locked := filepath.Join(root, "homes")
	writeFile(t, filepath.Join(locked, "f"), "x")
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	out.Reset()
	err = uninstall(true, false, nil)
	if !errors.As(err, &rep) || !strings.HasSuffix(out.String(), "error: ~/.proveo — not fully removed\n    unlinkat ~/.proveo/homes/f: permission denied\n") {
		t.Errorf("a root RemoveAll left ended on %v:\n%s", err, out)
	}
	if strings.Contains(out.String(), "root:") {
		t.Errorf("a root that stayed printed as removed:\n%s", out)
	}
}
