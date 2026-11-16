// SPEC: _spec/tests/testing-strategy.puml, _spec/tests/40-agent-e2e-components.puml
package schedule

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// TmuxRunner executes `tmux <args>` and returns combined output.
type TmuxRunner func(args ...string) (string, error)

func execTmux(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).CombinedOutput()
	return string(out), err
}

func TmuxAvailable() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// TmuxSession drives one detached tmux session.
type TmuxSession struct {
	Name string
	run  TmuxRunner
	pid  string // pane pid Pin saw; empty = any session under Name
}

func NewTmux(name string, run TmuxRunner) *TmuxSession {
	if run == nil {
		run = execTmux
	}
	return &TmuxSession{Name: sessionName.Replace(name), run: run}
}

// sessionName rewrites what tmux's target syntax reads as separators: "." is
// window.pane and ":" is session:window, so "hermes-qwen3.8" named no session.
var sessionName = strings.NewReplacer(".", "-", ":", "-")

// TmuxEnv is KEY=VALUE pairs every session Start launches gets, over what the tmux server holds.
var TmuxEnv []string

// Start launches cmd in a new detached session sized w×h (a fixed size keeps
// captures deterministic).
func (s *TmuxSession) Start(w, h int, cmd ...string) error {
	args := []string{"new-session", "-d", "-s", s.Name, "-x", strconv.Itoa(w), "-y", strconv.Itoa(h)}
	for _, kv := range TmuxEnv {
		args = append(args, "-e", kv)
	}
	args = append(args, cmd...)
	out, err := s.run(args...)
	if said := strings.TrimSpace(out); err != nil && said != "" {
		return fmt.Errorf("%w: %s", err, said)
	}
	return err
}

// SendText types literal text (no key-name interpretation).
func (s *TmuxSession) SendText(text string) error {
	_, err := s.run("send-keys", "-t", s.Name, "-l", text)
	return err
}

// Enter presses Enter.
func (s *TmuxSession) Enter() error {
	_, err := s.run("send-keys", "-t", s.Name, "Enter")
	return err
}

// SendKeys sends named keys to the session.
func (s *TmuxSession) SendKeys(keys ...string) error {
	_, err := s.run(append([]string{"send-keys", "-t", s.Name}, keys...)...)
	return err
}

// Capture returns the current rendered pane content.
func (s *TmuxSession) Capture() (string, error) {
	return s.run("capture-pane", "-p", "-t", s.Name)
}

func (s *TmuxSession) CaptureAll() (string, error) {
	return s.run("capture-pane", "-p", "-S", "-", "-t", s.Name)
}

func (s *TmuxSession) WaitFor(substr string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var last string
	for {
		if out, err := s.Capture(); err == nil {
			last = out
			if strings.Contains(out, substr) {
				return out, nil
			}
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("tmux: %q not seen within %s", substr, timeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// PanePID is the pid of the process the session's pane started.
func (s *TmuxSession) PanePID() (string, error) {
	return s.run("display-message", "-p", "-t", s.Name, "#{pane_pid}")
}

// Pin ties the session to the process its pane runs now.
// SPEC: _spec/internal/schedule/schedule.puml (ONE WATCHER PER SESSION)
func (s *TmuxSession) Pin() {
	if pid, err := s.PanePID(); err == nil {
		s.pid = strings.TrimSpace(pid)
	}
}

// Alive reports whether the session still exists and, once pinned, still runs the pinned pane.
func (s *TmuxSession) Alive() bool {
	if _, err := s.run("has-session", "-t", s.Name); err != nil {
		return false
	}
	if s.pid == "" {
		return true
	}
	pid, err := s.PanePID()
	return err == nil && strings.TrimSpace(pid) == s.pid
}

// Kill removes the session (best-effort; safe to call in cleanup).
func (s *TmuxSession) Kill() { _, _ = s.run("kill-session", "-t", s.Name) }
