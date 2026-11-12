//go:build !windows

// SPEC: _spec/internal/clean/clean-lifecycle.puml
package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// cleanHost is an isolated host for `proveo clean`: a temp HOME, proveo home and state root, and docker
// and sbx stubs first on PATH that answer with the given case arms and log each call to the returned file.
func cleanHost(t *testing.T, docker, sbxArms string) (calls string) {
	t.Helper()
	real, _ := os.UserHomeDir()
	isolatedHost(t)
	home := os.Getenv("PROVEO_HOME")
	if real == "" || strings.HasPrefix(home, real+string(os.PathSeparator)) || !strings.HasPrefix(home, os.Getenv("HOME")) {
		t.Fatalf("PROVEO_HOME %s is not under this test's temp HOME", home)
	}
	if err := os.MkdirAll(filepath.Join(os.Getenv("PROVEO_STATE_ROOT"), "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	calls = filepath.Join(bin, "calls")
	for name, arms := range map[string]string{"docker": docker, "sbx": sbxArms} {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> '" + calls + "'\ncase \"$*\" in\n" + arms + "\nesac\nexit 0\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// runState makes a run's state dir for each session id and returns their paths.
func runState(t *testing.T, sids ...string) []string {
	t.Helper()
	var dirs []string
	for _, sid := range sids {
		dir := filepath.Join(os.Getenv("PROVEO_STATE_ROOT"), "runs", sid)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, dir)
	}
	return dirs
}

// mkHome gives the proveo home one file, so --homes finds it.
func mkHome(t *testing.T) string {
	t.Helper()
	home := os.Getenv("PROVEO_HOME")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

const (
	deadRun   = "proveo-1700000000-999998"
	deadRun2  = "proveo-1700000000-999999"
	imageList = `  "image ls"*) echo proveo/x:test ;;`
)

func TestCleanPrintsWhatItRemovedAsOneBlockByKind(t *testing.T) {
	calls := cleanHost(t, imageList, "")
	dirs := runState(t, deadRun, deadRun2)
	home := mkHome(t)
	stdout, stderr, err := proveoOut(t, "clean", "--deep", "--homes")
	want := "state:  " + deadRun + "\n        " + deadRun2 + "\nimages: proveo/x:test\nhome:   ~/.proveo\nok: clean — 4 removed\n"
	if err != nil || stdout != "" || stderr != want {
		t.Errorf("clean = %v\n%s\nwant\n%s", err, stderr, want)
	}
	for _, d := range append(dirs, home) {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s survived clean (%v)", d, err)
		}
	}
	if b, _ := os.ReadFile(calls); !strings.Contains(string(b), "docker image rm proveo/x:test") {
		t.Errorf("the image was not removed:\n%s", b)
	}
}

func TestADryRunClosesOnNothingRemoved(t *testing.T) {
	calls := cleanHost(t, imageList, "")
	dirs := runState(t, deadRun)
	home := mkHome(t)
	_, stderr, err := proveoOut(t, "clean", "--deep", "--homes", "--dry-run")
	want := "state:  " + deadRun + "\nimages: proveo/x:test\nhome:   ~/.proveo\nclean — nothing removed (dry run)\n"
	if err != nil || stderr != want {
		t.Errorf("clean --dry-run = %v\n%s\nwant\n%s", err, stderr, want)
	}
	for _, d := range append(dirs, home) {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("a dry run removed %s", d)
		}
	}
	if b, _ := os.ReadFile(calls); strings.Contains(string(b), " rm ") {
		t.Errorf("a dry run called a removal:\n%s", b)
	}
}

func TestCleanWithNothingToRemoveSaysSoOnce(t *testing.T) {
	for _, args := range [][]string{{"clean"}, {"clean", "--homes"}, {"clean", "--deep", "--homes", "--dry-run"}} {
		cleanHost(t, "", "")
		if err := os.RemoveAll(os.Getenv("PROVEO_HOME")); err != nil {
			t.Fatal(err)
		}
		_, stderr, err := proveoOut(t, args...)
		if err != nil || stderr != "ok: clean — nothing to remove\n" {
			t.Errorf("proveo %s with nothing there = %v, %q", strings.Join(args, " "), err, stderr)
		}
	}
}

func TestCleanHomesNeverSaysNothingToCleanBeforeItRemovesTheHome(t *testing.T) {
	cleanHost(t, "", "")
	mkHome(t)
	_, stderr, err := proveoOut(t, "clean", "--homes")
	if err != nil || stderr != "home: ~/.proveo\nok: clean — 1 removed\n" {
		t.Errorf("clean --homes = %v, %q; want the home as a fact and one closing line", err, stderr)
	}
}

func TestWhatCleanLeftLiveIsOneWarningWithTheForceFix(t *testing.T) {
	cleanHost(t, "", "")
	live := "proveo-1700000000-" + strconv.Itoa(os.Getpid())
	dirs := runState(t, deadRun, live)
	_, stderr, err := proveoOut(t, "clean")
	want := "state: " + deadRun + "\nwarn: left: 1 that looks live\n    state " + live +
		"\n    `--force` removes it too and disrupts the run that holds it\nok: clean — 1 removed\n"
	if err != nil || stderr != want {
		t.Errorf("clean beside a live run = %v\n%s\nwant\n%s", err, stderr, want)
	}
	if strings.Count(stderr, "clean — ") != 1 {
		t.Errorf("clean prints more than one closing line:\n%s", stderr)
	}
	if _, err := os.Stat(dirs[1]); err != nil {
		t.Errorf("a live run's state dir was removed: %v", err)
	}
}

func TestOnlyWhatLooksLiveClosesOnNothingRemoved(t *testing.T) {
	for _, c := range []struct {
		name, sbx, why string
	}{
		{"a sandbox runs", `  ls*) printf 'SANDBOX AGENT STATUS\nproveo-claudecode-1a2b3c4d claude running\n' ;;`, ""},
		{"sbx gives no listing", `  ls*) exit 1 ;;`, "    sbx did not say whether a sandbox runs\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cleanHost(t, "", c.sbx)
			tools := filepath.Join(mkHome(t), ".go")
			if err := os.MkdirAll(tools, 0o700); err != nil {
				t.Fatal(err)
			}
			_, stderr, err := proveoOut(t, "clean", "--tools")
			want := "warn: left: 1 that looks live\n    tools ~/.proveo/.go\n" + c.why +
				"    `--force` removes it too and disrupts the run that holds it\nwarn: clean — nothing removed\n"
			if err != nil || stderr != want {
				t.Errorf("clean --tools = %v\n%s\nwant\n%s", err, stderr, want)
			}
			if _, err := os.Stat(tools); err != nil {
				t.Errorf("a toolchain was removed while it looked live: %v", err)
			}
		})
	}
}

func TestARemovalThatFailedIsAWarningAndExitsZero(t *testing.T) {
	cleanHost(t, imageList+"\n  \"image rm\"*) echo 'Error response from daemon: conflict: image is being used' >&2; exit 1 ;;", "")
	runState(t, deadRun)
	_, stderr, err := proveoOut(t, "clean", "--deep")
	want := "state: " + deadRun + "\nwarn: image proveo/x:test — not removed\n    Error response from daemon: conflict: image is being used\n" +
		"warn: clean — 1 not removed\n"
	if err != nil || stderr != want {
		t.Errorf("clean with a failed removal = %v\n%s\nwant\n%s", err, stderr, want)
	}
}

func TestAHomeThatStaysClosesOnACrossAndExitsOne(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes a file under a read-only dir")
	}
	cleanHost(t, "", "")
	home := mkHome(t)
	sealed := filepath.Join(home, ".claude")
	if err := os.WriteFile(filepath.Join(sealed, "session"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sealed, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o700) })
	_, stderr, err := proveoOut(t, "clean", "--homes")
	if code, ok := exitCode(err); !ok || code != 1 || !strings.HasPrefix(stderr, "warn: home ~/.proveo — not removed\n") ||
		!strings.HasSuffix(stderr, "error: clean — 1 not removed\n") {
		t.Errorf("clean --homes whose home stays = %v\n%s\nwant its ⚠, then × clean — 1 not removed, exit 1", err, stderr)
	}
}

func TestCleanToolsSaysWhatItReclaimedUnderTheClosingLine(t *testing.T) {
	cleanHost(t, "", `  ls*) printf 'SANDBOX AGENT STATUS\n' ;;`)
	tools := filepath.Join(mkHome(t), ".go")
	if err := os.MkdirAll(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "go"), make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := proveoOut(t, "clean", "--tools")
	if want := "tools: ~/.proveo/.go (2.0KB)\nok: clean — 1 removed\n    2.0KB of toolchains reclaimed\n"; err != nil || stderr != want {
		t.Errorf("clean --tools = %v\n%s\nwant\n%s", err, stderr, want)
	}
}
