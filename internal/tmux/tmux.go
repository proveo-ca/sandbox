// SPEC: _spec/tests/testing-strategy.puml, _spec/tests/40-agent-e2e-components.puml
package tmux

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner executes `tmux <args>` and returns combined output.
type Runner func(args ...string) (string, error)

func execRunner(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).CombinedOutput()
	return string(out), err
}

func Available() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// Session drives one detached tmux session.
type Session struct {
	Name string
	run  Runner
}

func New(name string, run Runner) *Session {
	if run == nil {
		run = execRunner
	}
	return &Session{Name: sessionName.Replace(name), run: run}
}

// sessionName rewrites what tmux's target syntax reads as separators: "." is
// window.pane and ":" is session:window, so "hermes-qwen3.8" named no session.
var sessionName = strings.NewReplacer(".", "-", ":", "-")

// Start launches cmd in a new detached session sized w×h (a fixed size keeps
// captures deterministic).
func (s *Session) Start(w, h int, cmd ...string) error {
	args := append([]string{"new-session", "-d", "-s", s.Name, "-x", strconv.Itoa(w), "-y", strconv.Itoa(h)}, cmd...)
	_, err := s.run(args...)
	return err
}

// SendText types literal text (no key-name interpretation).
func (s *Session) SendText(text string) error {
	_, err := s.run("send-keys", "-t", s.Name, "-l", text)
	return err
}

// Enter presses Enter.
func (s *Session) Enter() error {
	_, err := s.run("send-keys", "-t", s.Name, "Enter")
	return err
}

// SendKeys sends named keys to the session.
func (s *Session) SendKeys(keys ...string) error {
	_, err := s.run(append([]string{"send-keys", "-t", s.Name}, keys...)...)
	return err
}

// Capture returns the current rendered pane content.
func (s *Session) Capture() (string, error) {
	return s.run("capture-pane", "-p", "-t", s.Name)
}

func (s *Session) CaptureAll() (string, error) {
	return s.run("capture-pane", "-p", "-S", "-", "-t", s.Name)
}

func (s *Session) WaitFor(substr string, timeout time.Duration) (string, error) {
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
func (s *Session) PanePID() (string, error) {
	return s.run("display-message", "-p", "-t", s.Name, "#{pane_pid}")
}

// Alive reports whether the session still exists.
func (s *Session) Alive() bool {
	_, err := s.run("has-session", "-t", s.Name)
	return err == nil
}

// Kill removes the session (best-effort; safe to call in cleanup).
func (s *Session) Kill() { _, _ = s.run("kill-session", "-t", s.Name) }
