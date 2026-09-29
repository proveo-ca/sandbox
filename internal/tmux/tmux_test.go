package tmux

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// recorder is a fake Runner: it records the args of each call and returns
// scripted stdout (one entry per call, last repeats).
type recorder struct {
	calls   [][]string
	outputs []string
	n       int
}

func (r *recorder) run(args ...string) (string, error) {
	r.calls = append(r.calls, args)
	if len(r.outputs) == 0 {
		return "", nil
	}
	i := r.n
	if i >= len(r.outputs) {
		i = len(r.outputs) - 1
	}
	r.n++
	return r.outputs[i], nil
}

func TestCommandArgs(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	s := New("sess", rec.run)
	if err := s.Start(200, 50, "docker", "run", "-it", "img"); err != nil {
		t.Fatal(err)
	}
	_ = s.SendText("hello world")
	_ = s.Enter()
	_ = s.SendKeys("C-c")
	_, _ = s.Capture()
	s.Kill()

	want := [][]string{
		{"new-session", "-d", "-s", "sess", "-x", "200", "-y", "50", "docker", "run", "-it", "img"},
		{"send-keys", "-t", "sess", "-l", "hello world"},
		{"send-keys", "-t", "sess", "Enter"},
		{"send-keys", "-t", "sess", "C-c"},
		{"capture-pane", "-p", "-t", "sess"},
		{"kill-session", "-t", "sess"},
	}
	if diff := cmp.Diff(want, rec.calls); diff != "" {
		t.Errorf("tmux command args mismatch (-want +got):\n%s", diff)
	}
}

func TestWaitForFindsText(t *testing.T) {
	t.Parallel()
	rec := &recorder{outputs: []string{"booting...", "booting...", "READY >"}}
	s := New("sess", rec.run)
	got, err := s.WaitFor("READY", 5*time.Second)
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if got != "READY >" {
		t.Errorf("WaitFor returned %q, want the matching capture", got)
	}
}

func TestWaitForTimeout(t *testing.T) {
	t.Parallel()
	rec := &recorder{outputs: []string{"still working"}}
	s := New("sess", rec.run)
	if _, err := s.WaitFor("DONE", 10*time.Millisecond); err == nil {
		t.Fatal("WaitFor should time out when the substring never appears")
	}
}

func TestWaitForToleratesCaptureError(t *testing.T) {
	t.Parallel()
	// A capture error should not abort the poll (transient during startup).
	errThenText := &flakyRunner{errs: 1, out: "READY"}
	s := New("sess", errThenText.run)
	if _, err := s.WaitFor("READY", 5*time.Second); err != nil {
		t.Fatalf("WaitFor should recover after a transient capture error: %v", err)
	}
}

type flakyRunner struct {
	errs int
	out  string
	n    int
}

func (f *flakyRunner) run(...string) (string, error) {
	f.n++
	if f.n <= f.errs {
		return "", errors.New("no server running")
	}
	return f.out, nil
}

func TestNewNamesASessionTmuxCanTarget(t *testing.T) {
	t.Parallel()
	if got := New("proveo-e2e-hermes-qwen3.8-42", nil).Name; got != "proveo-e2e-hermes-qwen3-8-42" {
		t.Errorf("Name = %q — tmux reads '.' as window.pane, so capture-pane answered \"can't find pane\"", got)
	}
	if got := New("a:b", nil).Name; got != "a-b" {
		t.Errorf("Name = %q — ':' separates session from window", got)
	}
}
