// SPEC: _spec/internal/schedule/schedule.puml
package schedule

import (
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

// Install registers the minutely tick with the host's service manager.
func Install(home, proveo string, report func(string, ...any)) error {
	path := os.Getenv("PATH")
	switch runtime.GOOS {
	case "darwin":
		plist, err := launchdPath()
		if err != nil {
			return err
		}
		logFile := filepath.Join(home, "logs", "schedule", "tick.log")
		if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(plist, []byte(LaunchdPlist(proveo, path, logFile)), 0o644); err != nil {
			return err
		}
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		_ = exec.Command("launchctl", "bootout", domain+"/"+LaunchdLabel).Run()
		if out, err := exec.Command("launchctl", "bootstrap", domain, plist).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
		}
		report("schedule: LaunchAgent %s loaded — `proveo schedule tick` runs every minute (log %s)", plist, logFile)
		return nil
	case "linux":
		dir, err := systemdDir()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		svc, tmr := SystemdUnits(proveo, path)
		if err := os.WriteFile(filepath.Join(dir, systemdUnit+".service"), []byte(svc), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, systemdUnit+".timer"), []byte(tmr), 0o644); err != nil {
			return err
		}
		for _, args := range [][]string{{"--user", "daemon-reload"}, {"--user", "enable", "--now", systemdUnit + ".timer"}} {
			if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
				return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			}
		}
		report("schedule: systemd user timer %s.timer enabled — `proveo schedule tick` runs every minute", systemdUnit)
		return nil
	}
	return fmt.Errorf("schedule install supports macOS (launchd) and Linux (systemd --user); on %s run `%s schedule tick` every minute yourself", runtime.GOOS, proveo)
}

// Uninstall removes the tick from the host's service manager.
func Uninstall(report func(string, ...any)) error {
	switch runtime.GOOS {
	case "darwin":
		plist, err := launchdPath()
		if err != nil {
			return err
		}
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), LaunchdLabel)).Run()
		if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
			return err
		}
		report("schedule: LaunchAgent removed")
	case "linux":
		_ = exec.Command("systemctl", "--user", "disable", "--now", systemdUnit+".timer").Run()
		dir, err := systemdDir()
		if err != nil {
			return err
		}
		for _, f := range []string{systemdUnit + ".service", systemdUnit + ".timer"} {
			if err := os.Remove(filepath.Join(dir, f)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		report("schedule: systemd user timer removed")
	}
	return nil
}
