// SPEC: _spec/internal/schedule/schedule.puml
package schedule

import (
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/ui"
)

func TestRowsListEveryEntrySoonestFirst(t *testing.T) {
	t.Parallel()
	loc := ny(t)
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, loc) // Sun 21:00 EDT
	c := Config{Jobs: map[string]Job{
		"muse-lineup":  {Target: "hermes", Model: "muse-glimmer:30b-mlx", PromptFile: "p", Budget: "45m", At: []string{"sun 11:35", "mon 18:50"}},
		"muse-waivers": {Target: "hermes", PromptFile: "p", Budget: "1h", At: []string{"tue 20:00"}},
		"probe":        {Command: []string{"true"}, PromptFile: "p", Mode: "plain", At: []string{"mon 09:00"}},
	}}
	running := func(job string) bool { return job == "muse-waivers" }
	last := func(job string) *Result {
		if job == "muse-lineup" {
			return &Result{Started: time.Date(2026, 9, 27, 11, 35, 0, 0, loc), Outcome: "ended"}
		}
		return nil
	}
	rows := Rows(c, now, running, last)
	var order []string
	for _, r := range rows {
		order = append(order, r.Job+" "+r.Entry)
	}
	want := []string{"probe mon 09:00", "muse-lineup mon 18:50", "muse-waivers tue 20:00", "muse-lineup sun 11:35"}
	if strings.Join(order, ", ") != strings.Join(want, ", ") {
		t.Fatalf("order = %v\nwant    %v", order, want)
	}
	byKey := map[string]Row{}
	for _, r := range rows {
		byKey[r.Job+" "+r.Entry] = r
	}
	if r := byKey["muse-waivers tue 20:00"]; r.Status != "running" || r.Model != "-" || r.Budget != "1h" || r.Mode != "goal" {
		t.Errorf("waivers row = %+v", r)
	}
	if r := byKey["muse-lineup mon 18:50"]; r.Last != "Sun Sep 27 11:35 ended · no report" || r.Budget != "45m" {
		t.Errorf("lineup row = %+v", r)
	}
	if r := byKey["probe mon 09:00"]; r.Target != "command" {
		t.Errorf("a command job shows target %q, want command", r.Target)
	}
}

func TestRenderGroupsByJobAndMarksTheNextRun(t *testing.T) {
	t.Parallel()
	loc := ny(t)
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, loc)
	c := Config{Jobs: map[string]Job{
		"muse-lineup":  {Target: "hermes", Model: "m", PromptFile: "p", At: []string{"mon 18:50", "thu 18:50"}},
		"muse-waivers": {Target: "hermes", Model: "m", PromptFile: "p", Budget: "1h", At: []string{"tue 20:00"}},
	}}
	var b strings.Builder
	p := ui.New(&b)
	p.Tier = ui.GlyphsNerd
	Render(p, Rows(c, now, nil, nil), now, Tick{Installed: true, Detail: "launchd"}, func(j string) string { return "~/logs/" + j })
	out := b.String()
	for _, want := range []string{
		"schedule", "tick installed: launchd",
		"muse-lineup", "hermes · m · goal · 45m budget · idle · last never",
		"▸ Mon Sep 28 18:50 EDT  in 21h 50m  mon 18:50",
		"Thu Oct 1 18:50 EDT", "muse-waivers", "1h budget", "transcripts ~/logs/muse-waivers",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "▸") != 1 {
		t.Errorf("exactly one run is next, got:\n%s", out)
	}
	if strings.Index(out, "muse-lineup") > strings.Index(out, "muse-waivers") {
		t.Errorf("the job with the soonest run comes first:\n%s", out)
	}

	b.Reset()
	Render(ui.New(&b), nil, now, Tick{Detail: "`proveo schedule install`"}, nil)
	if !strings.Contains(b.String(), "tick not installed") || !strings.Contains(b.String(), "nothing scheduled") {
		t.Errorf("empty, uninstalled schedule: %q", b.String())
	}
}

func TestUntilAndCompact(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		30 * time.Second: "now", 45 * time.Minute: "in 45m", 21*time.Hour + 50*time.Minute: "in 21h 50m",
		46*time.Hour + 10*time.Minute: "in 1d 22h",
	} {
		if got := until(now.Add(d), now); got != want {
			t.Errorf("until(%s) = %q, want %q", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{45 * time.Minute: "45m", time.Hour: "1h", 90 * time.Minute: "1h30m"} {
		if got := compact(d); got != want {
			t.Errorf("compact(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestRenderFlagsAFailedLastRunWithTheRetryCommand(t *testing.T) {
	t.Parallel()
	loc := ny(t)
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, loc)
	c := Config{Jobs: map[string]Job{
		"muse-lineup":  {Target: "hermes", PromptFile: "p", At: []string{"mon 18:50"}},
		"muse-waivers": {Target: "hermes", PromptFile: "p", At: []string{"tue 20:00"}},
		"muse-trades":  {Target: "hermes", PromptFile: "p", At: []string{"wed 20:00"}},
	}}
	failed := &Result{Entry: "sun 11:35", Started: time.Date(2026, 9, 27, 11, 35, 0, 0, loc), Outcome: "failed",
		Detail: "the agent exited 1 before the budget\nmore"}
	last := func(job string) *Result {
		if job == "muse-trades" {
			return &Result{Started: failed.Started, Outcome: "budget"}
		}
		return failed
	}
	running := func(job string) bool { return job == "muse-waivers" }
	rows := Rows(c, now, running, last)

	got := FailedJobs(rows)
	if len(got) != 1 || got[0].Job != "muse-lineup" || got[0].Failed.Entry != "sun 11:35" {
		t.Fatalf("failed jobs = %+v, want only muse-lineup (waivers runs again, trades hit its budget)", got)
	}
	var b strings.Builder
	p := ui.New(&b)
	p.Plain = true
	Render(p, rows, now, Tick{Installed: true, Detail: "launchd"}, nil)
	out := b.String()
	want := "failed: the agent exited 1 before the budget — `proveo schedule retry muse-lineup`"
	if !strings.Contains(out, want) {
		t.Errorf("render missing %q:\n%s", want, out)
	}
	if strings.Count(out, "proveo schedule retry") != 1 {
		t.Errorf("only the failed, idle job offers a retry:\n%s", out)
	}
}

func TestRowsShowTheReportSummary(t *testing.T) {
	t.Parallel()
	loc := ny(t)
	now := time.Date(2026, 9, 28, 21, 0, 0, 0, loc)
	c := Config{Jobs: map[string]Job{"lineup": {Target: "hermes", PromptFile: "p", At: []string{"thu 18:50"}}}}
	last := func(string) *Result {
		return &Result{Started: time.Date(2026, 9, 28, 19, 10, 0, 0, loc), Outcome: "budget", Summary: "no-op — every Week 3 slot is locked"}
	}
	if r := Rows(c, now, nil, last)[0]; r.Last != "Mon Sep 28 19:10 budget · no-op — every Week 3 slot is locked" {
		t.Errorf("last = %q", r.Last)
	}
}
