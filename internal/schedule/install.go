// SPEC: _spec/internal/schedule/schedule.puml
package schedule

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	LaunchdLabel = "io.proveo.schedule"
	systemdUnit  = "proveo-schedule"
)

// Runner runs a service-manager command and returns its combined output.
type Runner func(name string, args ...string) ([]byte, error)

// ExecRunner runs the command on this host.
func ExecRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// ErrNoServiceManager is a host proveo registers no tick with.
var ErrNoServiceManager = errors.New("no service manager")

// LaunchdPlist is the LaunchAgent that runs `proveo schedule tick` every minute.
func LaunchdPlist(proveo, path, logFile string) string {
	esc := func(s string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + LaunchdLabel + `</string>
  <key>ProgramArguments</key>
  <array><string>` + esc(proveo) + `</string><string>schedule</string><string>tick</string></array>
  <key>StartInterval</key><integer>60</integer>
  <key>RunAtLoad</key><true/>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>` + esc(path) + `</string></dict>
  <key>StandardOutPath</key><string>` + esc(logFile) + `</string>
  <key>StandardErrorPath</key><string>` + esc(logFile) + `</string>
</dict>
</plist>
`
}

// SystemdUnits are the user service and minutely timer that run the tick.
func SystemdUnits(proveo, path string) (service, timer string) {
	service = "[Unit]\nDescription=proveo scheduled runs (tick)\n\n[Service]\nType=oneshot\n" +
		"Environment=PATH=" + path + "\nExecStart=" + proveo + " schedule tick\n"
	timer = "[Unit]\nDescription=proveo scheduled runs, every minute\n\n[Timer]\nOnCalendar=minutely\n" +
		"Persistent=true\n\n[Install]\nWantedBy=timers.target\n"
	return service, timer
}

func launchdPath() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "Library", "LaunchAgents", LaunchdLabel+".plist"), nil
}

func systemdDir() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".config", "systemd", "user"), nil
}

// TickFiles are the files that register the tick on this OS: the LaunchAgent plist, or the systemd
// service and timer; none where proveo registers no tick.
func TickFiles() []string {
	switch runtime.GOOS {
	case "darwin":
		if p, err := launchdPath(); err == nil {
			return []string{p}
		}
	case "linux":
		if dir, err := systemdDir(); err == nil {
			return []string{filepath.Join(dir, systemdUnit+".service"), filepath.Join(dir, systemdUnit+".timer")}
		}
	}
	return nil
}

// RemoveCommand is the shell command that unregisters the tick and deletes TickFiles without proveo, each
// file written by path; "" where proveo registers no tick.
func RemoveCommand(path func(string) string) string {
	files := TickFiles()
	if len(files) == 0 {
		return ""
	}
	for i := range files {
		files[i] = path(files[i])
	}
	switch runtime.GOOS {
	case "darwin":
		return fmt.Sprintf("launchctl bootout gui/%d/%s; rm %s", os.Getuid(), LaunchdLabel, strings.Join(files, " "))
	case "linux":
		return "systemctl --user disable --now " + systemdUnit + ".timer; rm " + strings.Join(files, " ")
	}
	return ""
}

// tickHome is the proveo home a registered tick reads.
func tickHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".proveo")
}

// Install registers the minutely tick with the host's service manager and says how it runs.
func Install(home, proveo string, run Runner) (Tick, error) {
	path := os.Getenv("PATH")
	switch runtime.GOOS {
	case "darwin":
		plist, err := launchdPath()
		if err != nil {
			return Tick{}, err
		}
		logFile := filepath.Join(home, "logs", "schedule", "tick.log")
		if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
			return Tick{}, err
		}
		if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
			return Tick{}, err
		}
		if err := os.WriteFile(plist, []byte(LaunchdPlist(proveo, path, logFile)), 0o644); err != nil {
			return Tick{}, err
		}
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		_, _ = run("launchctl", "bootout", domain+"/"+LaunchdLabel)
		if out, err := run("launchctl", "bootstrap", domain, plist); err != nil {
			return Tick{}, fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return Tick{Installed: true, Manager: launchdManager, Log: logFile, Home: tickHome()}, nil
	case "linux":
		dir, err := systemdDir()
		if err != nil {
			return Tick{}, err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Tick{}, err
		}
		svc, tmr := SystemdUnits(proveo, path)
		if err := os.WriteFile(filepath.Join(dir, systemdUnit+".service"), []byte(svc), 0o644); err != nil {
			return Tick{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, systemdUnit+".timer"), []byte(tmr), 0o644); err != nil {
			return Tick{}, err
		}
		for _, args := range [][]string{{"--user", "daemon-reload"}, {"--user", "enable", "--now", systemdUnit + ".timer"}} {
			if out, err := run("systemctl", args...); err != nil {
				return Tick{}, fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			}
		}
		return Tick{Installed: true, Manager: systemdManager, Home: tickHome()}, nil
	}
	return Tick{NoManager: runtime.GOOS}, ErrNoServiceManager
}

// Uninstall removes the tick from the host's service manager; removed says whether one was there.
func Uninstall(run Runner) (removed bool, err error) {
	switch runtime.GOOS {
	case "darwin":
		if _, err := run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), LaunchdLabel)); err == nil {
			removed = true
		}
	case "linux":
		if _, err := run("systemctl", "--user", "disable", "--now", systemdUnit+".timer"); err == nil {
			removed = true
		}
	default:
		return false, nil
	}
	for _, f := range TickFiles() {
		switch err := os.Remove(f); {
		case err == nil:
			removed = true
		case !os.IsNotExist(err):
			return removed, err
		}
	}
	return removed, nil
}

const (
	launchdManager = "launchd " + LaunchdLabel + ", every 60s"
	systemdManager = "systemd --user " + systemdUnit + ".timer, every minute"
)

// TickStatus is the minutely tick as the host's service manager holds it.
func TickStatus(run Runner) Tick {
	switch runtime.GOOS {
	case "darwin":
		t := Tick{Install: "`proveo schedule install` registers it with launchd", Home: tickHome()}
		out, err := run("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), LaunchdLabel))
		if err != nil {
			return t
		}
		t.Installed, t.Manager = true, launchdManager
		t.Exit, t.Log = parseLaunchdPrint(string(out))
		return t
	case "linux":
		t := Tick{Install: "`proveo schedule install` registers it with systemd --user", Home: tickHome()}
		out, _ := run("systemctl", "--user", "is-active", systemdUnit+".timer")
		if strings.TrimSpace(string(out)) == "active" {
			t.Installed, t.Manager = true, systemdManager
		}
		return t
	}
	return Tick{NoManager: runtime.GOOS}
}

// parseLaunchdPrint reads `launchctl print`'s job: the last exit status when it was not 0, and the
// file the job writes its output to.
func parseLaunchdPrint(out string) (exit, log string) {
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(l, "last exit code = "); ok && exit == "" {
			if v = strings.TrimSpace(v); v != "0" && !strings.HasPrefix(v, "(") {
				exit = v
			}
		}
		if v, ok := strings.CutPrefix(l, "stdout path = "); ok && log == "" {
			log = strings.TrimSpace(v)
		}
	}
	return exit, log
}
