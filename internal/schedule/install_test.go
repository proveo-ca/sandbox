// SPEC: _spec/internal/schedule/schedule.puml
package schedule

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeManager stands in for launchctl and systemctl: it records each call and fails the ones fail names.
type fakeManager struct {
	calls []string
	fail  map[string]bool
	print string
}

func (f *fakeManager) run(name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	for verb := range f.fail {
		if strings.Contains(call, verb) {
			return []byte("Boot-out failed: 3: No such process"), errors.New("exit status 3")
		}
	}
	if len(args) > 0 && args[0] == "print" {
		return []byte(f.print), nil
	}
	return nil, nil
}

// testsHome points HOME at a temp dir and returns it, after noting how the operator's own tick files
// stand so the test can prove it left them alone.
func testsHome(t *testing.T) (home string, untouched func()) {
	t.Helper()
	real := TickFiles()
	before := make([]string, len(real))
	for i, f := range real {
		before[i] = stamp(f)
	}
	home = t.TempDir()
	t.Setenv("HOME", home)
	return home, func() {
		t.Helper()
		for i, f := range real {
			if stamp(f) != before[i] {
				t.Errorf("the operator's %s changed", f)
			}
		}
	}
}

func stamp(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "absent"
	}
	return fi.ModTime().Format(time.RFC3339Nano)
}

func TestInstallAndUninstallTouchOnlyTheTestsHome(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("proveo registers no tick on " + runtime.GOOS)
	}
	home, untouched := testsHome(t)
	defer untouched()
	f := &fakeManager{}
	tick, err := Install(filepath.Join(home, ".proveo"), "/opt/proveo", f.run)
	if err != nil {
		t.Fatalf("Install = %v", err)
	}
	if !tick.Installed || tick.Manager == "" || tick.Home != filepath.Join(home, ".proveo") {
		t.Errorf("Install says %+v", tick)
	}
	files := TickFiles()
	for _, file := range files {
		if !strings.HasPrefix(file, home+string(os.PathSeparator)) {
			t.Fatalf("a tick file outside the test's HOME: %s", file)
		}
		if _, err := os.Stat(file); err != nil {
			t.Errorf("Install wrote no %s: %v", file, err)
		}
	}
	if runtime.GOOS == "darwin" && tick.Log != filepath.Join(home, ".proveo", "logs", "schedule", "tick.log") {
		t.Errorf("the tick logs to %q", tick.Log)
	}

	removed, err := Uninstall(f.run)
	if err != nil || !removed {
		t.Fatalf("Uninstall = %v, %v; want the tick it found removed", removed, err)
	}
	for _, file := range files {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Errorf("Uninstall left %s", file)
		}
	}
	gone := &fakeManager{fail: map[string]bool{"bootout": true, "disable": true}}
	if removed, err := Uninstall(gone.run); err != nil || removed {
		t.Errorf("Uninstall with nothing there = %v, %v; want nothing removed", removed, err)
	}
}

func TestUninstallCountsATickTheServiceManagerHeldWithoutItsFile(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("proveo registers no tick on " + runtime.GOOS)
	}
	_, untouched := testsHome(t)
	defer untouched()
	f := &fakeManager{}
	if removed, err := Uninstall(f.run); err != nil || !removed {
		t.Errorf("Uninstall = %v, %v; want the tick the service manager let go of counted", removed, err)
	}
	if len(f.calls) != 1 || !strings.Contains(f.calls[0], "bootout") && !strings.Contains(f.calls[0], "disable") {
		t.Errorf("calls = %q", f.calls)
	}
}

func TestALaunchdPrintWithANonZeroLastExitReadsAsFailing(t *testing.T) {
	t.Parallel()
	for print, want := range map[string][2]string{
		"\tstdout path = /u/.proveo/logs/schedule/tick.log\n\tlast exit code = 0\n": {"", "/u/.proveo/logs/schedule/tick.log"},
		"\tlast exit code = 1\n\tstdout path = /u/x.log\n":                          {"1", "/u/x.log"},
		"\tlast exit code = (never exited)\n":                                       {"", ""},
		"\tlast exit code = 78: EX_CONFIG\n":                                        {"78: EX_CONFIG", ""},
	} {
		if exit, log := parseLaunchdPrint(print); exit != want[0] || log != want[1] {
			t.Errorf("parseLaunchdPrint(%q) = %q, %q; want %q, %q", print, exit, log, want[0], want[1])
		}
	}
}

func TestTickStatusReadsTheJobLaunchdHolds(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd is macOS's")
	}
	home, untouched := testsHome(t)
	defer untouched()
	f := &fakeManager{print: "\tstdout path = /l/tick.log\n\tlast exit code = 1\n"}
	got := TickStatus(f.run)
	if !got.Installed || got.Exit != "1" || got.Log != "/l/tick.log" || got.Home != filepath.Join(home, ".proveo") {
		t.Errorf("TickStatus = %+v", got)
	}
	if missing := TickStatus((&fakeManager{fail: map[string]bool{"print": true}}).run); missing.Installed || missing.Install == "" ||
		missing.Home != filepath.Join(home, ".proveo") {
		t.Errorf("an unregistered tick = %+v, want not installed with its how-to and the home a tick would read", missing)
	}
}
