// SPEC: _spec/internal/schedule/schedule.puml
package schedule

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func ny(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestParseAtReadsWeekdayAndClock(t *testing.T) {
	t.Parallel()
	wd, h, m, err := parseAt("Thu 18:45")
	if err != nil || wd != time.Thursday || h != 18 || m != 45 {
		t.Errorf("parseAt = %v %d:%d %v", wd, h, m, err)
	}
	for _, bad := range []string{"thu", "xyz 10:00", "sun 25:00", "mon 9", "tue 10:61"} {
		if _, _, _, err := parseAt(bad); err == nil {
			t.Errorf("parseAt(%q) must fail", bad)
		}
	}
}

func TestOccurrencesAreInTheJobsTimezone(t *testing.T) {
	t.Parallel()
	loc := ny(t)
	// Sunday 2026-09-27 16:00 EDT.
	now := time.Date(2026, 9, 27, 16, 0, 0, 0, loc)
	last, _ := lastOccurrence("sun 11:35", now, loc)
	if want := time.Date(2026, 9, 27, 11, 35, 0, 0, loc); !last.Equal(want) {
		t.Errorf("last = %v, want %v", last, want)
	}
	last, _ = lastOccurrence("sun 18:55", now, loc)
	if want := time.Date(2026, 9, 20, 18, 55, 0, 0, loc); !last.Equal(want) {
		t.Errorf("an entry later today must resolve to last week: %v, want %v", last, want)
	}
	next, _ := NextOccurrence("mon 18:50", now, loc)
	if want := time.Date(2026, 9, 28, 18, 50, 0, 0, loc); !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
	// The clock time holds across the DST change (Nov 1 2026, EDT → EST).
	after := time.Date(2026, 11, 2, 12, 0, 0, 0, loc)
	last, _ = lastOccurrence("sun 11:35", after, loc)
	if last.Hour() != 11 || last.Minute() != 35 || last.Day() != 1 {
		t.Errorf("across DST: %v, want Sun Nov 1 11:35 local", last)
	}
}

func TestDueFiresOnceWithinCatchUp(t *testing.T) {
	t.Parallel()
	loc := ny(t)
	c := Config{Jobs: map[string]Job{"lineup": {Target: "hermes", PromptFile: "p", At: []string{"sun 11:35", "sun 15:00"}}}}
	st := State{}

	early := time.Date(2026, 9, 27, 11, 34, 0, 0, loc)
	if d := c.Due(early, st); len(d) != 0 {
		t.Errorf("a minute early nothing is due: %v", d)
	}
	on := time.Date(2026, 9, 27, 11, 36, 0, 0, loc)
	d := c.Due(on, st)
	if len(d) != 1 || d[0].Entry != "sun 11:35" {
		t.Fatalf("due = %v, want sun 11:35", d)
	}
	st.Mark(d[0].Job, d[0].Entry, d[0].At)
	if again := c.Due(on.Add(time.Minute), st); len(again) != 0 {
		t.Errorf("a fired entry must not fire twice: %v", again)
	}
	late := time.Date(2026, 9, 27, 12, 0, 0, 0, loc)
	if d := c.Due(late, State{}); len(d) != 0 {
		t.Errorf("25 min late is past the 20 min catch-up: %v", d)
	}
	next := time.Date(2026, 10, 4, 11, 40, 0, 0, loc)
	if d := c.Due(next, st); len(d) != 1 {
		t.Errorf("next week the entry fires again: %v", d)
	}
}

func TestInstructionFlattensThePromptUnderTheMode(t *testing.T) {
	t.Parallel()
	prompt := "Line one.\n\n  - item two\n"
	for mode, want := range map[string]string{
		"":      "/goal Line one. - item two",
		"goal":  "/goal Line one. - item two",
		"loop":  "/loop Line one. - item two",
		"plain": "Line one. - item two",
	} {
		if got := (Job{Mode: mode}).Instruction(prompt); got != want {
			t.Errorf("mode %q: %q, want %q", mode, got, want)
		}
	}
}

func TestRunArgvIsUnattended(t *testing.T) {
	t.Parallel()
	j := Job{Target: "hermes", Model: "qwen3.8:27b-mlx", Addons: []string{"host-chrome"}, Args: []string{"--egress-mode", "open"}}
	got := j.RunArgv("/bin/proveo")
	want := []string{"/bin/proveo", "run", "hermes", "--yes", "--local-model", "qwen3.8:27b-mlx", "--addon", "host-chrome", "--egress-mode", "open"}
	if !slices.Equal(got, want) {
		t.Errorf("argv = %v\nwant   %v", got, want)
	}
}

func TestLoadValidatesJobs(t *testing.T) {
	home := t.TempDir()
	write := func(s string) {
		if err := os.WriteFile(Path(home), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("jobs:\n  a:\n    target: hermes\n    prompt_file: p.md\n    at: [\"sun 11:35\"]\n")
	if _, err := Load(home); err != nil {
		t.Errorf("valid file: %v", err)
	}
	write("jobs:\n  a:\n    target: hermes\n    prompt_file: p.md\n    at: [\"someday\"]\n")
	if _, err := Load(home); err == nil || !strings.Contains(err.Error(), "someday") {
		t.Errorf("bad at must name it: %v", err)
	}
	write("jobs:\n  a:\n    target: hermes\n    prompt_file: p.md\n    mode: forever\n    at: []\n")
	if _, err := Load(home); err == nil {
		t.Error("unknown mode must be refused")
	}
}

func TestLaunchdPlistRunsTheTickEveryMinute(t *testing.T) {
	t.Parallel()
	p := LaunchdPlist("/Users/a&b/proveo", "/usr/bin:/bin", "/tmp/tick.log")
	for _, want := range []string{"<string>" + LaunchdLabel + "</string>", "<string>/Users/a&amp;b/proveo</string>",
		"<string>schedule</string><string>tick</string>", "<key>StartInterval</key><integer>60</integer>", "/usr/bin:/bin"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q", want)
		}
	}
}

// fakeTmux scripts a pane: it shows the ready marker, records what is typed, and ends when told.
type fakeTmux struct {
	mu     sync.Mutex
	pane   string
	typed  []string
	alive  bool
	endOn  string
	killed bool
}

func (f *fakeTmux) run(args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch args[0] {
	case "has-session":
		if f.alive {
			return "", nil
		}
		return "", os.ErrNotExist
	case "capture-pane":
		return f.pane, nil
	case "send-keys":
		if args[3] == "-l" {
			f.typed = append(f.typed, args[4])
			if f.endOn != "" && strings.HasPrefix(args[4], f.endOn) {
				f.alive = false
			}
		}
		return "", nil
	case "kill-session":
		f.killed, f.alive = true, false
	}
	return "", nil
}

func TestWatchTypesTheGoalOnceTheAgentIsReady(t *testing.T) {
	home := t.TempDir()
	prompt := filepath.Join(home, "p.md")
	if err := os.WriteFile(prompt, []byte("Set the best lineup.\nBench anyone Out."), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeTmux{pane: "hermes ready\n❯ ", alive: true, endOn: "/goal"}
	j := Job{Target: "hermes", PromptFile: prompt, Budget: "1m"}
	var note string
	res := Watch(home, "lineup", "sun 11:35", "t.log", j, f.run, func(_, body string) { note = body })

	if len(f.typed) == 0 || f.typed[0] != "/goal Set the best lineup. Bench anyone Out." {
		t.Fatalf("typed = %q", f.typed)
	}
	if res.Outcome != "ended" || !strings.HasPrefix(note, "ended") {
		t.Errorf("outcome = %q (%s), note %q", res.Outcome, res.Detail, note)
	}
	if last := LastResult(home, "lineup"); last == nil || last.Entry != "sun 11:35" {
		t.Errorf("the result must be recorded: %+v", last)
	}
}

func TestCommandReplacesTheHarnessRun(t *testing.T) {
	t.Parallel()
	j := Job{Command: []string{"sh", "-c", "read x"}, Model: "ignored"}
	if got := j.RunArgv("/bin/proveo"); !slices.Equal(got, []string{"sh", "-c", "read x"}) {
		t.Errorf("argv = %v", got)
	}
	if err := (Job{Command: []string{"true"}, PromptFile: "p"}).validate(); err != nil {
		t.Errorf("a command job needs no target: %v", err)
	}
}
