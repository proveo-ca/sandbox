// SPEC: _spec/internal/schedule/schedule.puml
package schedule

import (
	"path/filepath"
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
	running := func(job string) *Live {
		if job == "muse-waivers" {
			return &Live{}
		}
		return nil
	}
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

// plainRender is Render on a plain printer: what a pipe reads.
func plainRender(rows []Row, l Listing) string {
	var b strings.Builder
	p := ui.New(&b)
	p.Plain = true
	Render(p, rows, l)
	return b.String()
}

// installedTick is a tick launchd holds for home.
func installedTick(home string) Tick {
	return Tick{Installed: true, Manager: launchdManager, Home: home}
}

func TestTheScheduleListsEachJobAsOneFactSoonestFirst(t *testing.T) {
	t.Parallel()
	loc := ny(t)
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, loc)
	c := Config{Jobs: map[string]Job{
		"muse-lineup":  {Target: "hermes", Model: "m", PromptFile: "p", At: []string{"mon 18:50", "thu 18:50"}},
		"muse-waivers": {Target: "hermes", PromptFile: "p", Budget: "1h", At: []string{"tue 20:00"}},
	}}
	last := func(job string) *Result {
		if job == "muse-waivers" {
			return &Result{Started: time.Date(2026, 9, 22, 20, 0, 0, 0, loc), Outcome: "budget", Summary: "two claims placed"}
		}
		return nil
	}
	got := plainRender(Rows(c, now, nil, last), Listing{Now: now, Home: "/h/.proveo", Tick: installedTick("/h/.proveo")})
	want := strings.Join([]string{
		"muse-lineup:  Mon Sep 28 18:50 EDT · in 21h 50m",
		"              Thu Oct 1 18:50 EDT · in 3d 21h",
		"                hermes · m · goal · 45m budget",
		"muse-waivers: Tue Sep 29 20:00 EDT · in 1d 23h",
		"              hermes · goal · 1h budget",
		"              last Tue Sep 22 20:00 budget · two claims placed",
		"ok: tick — installed",
		"    launchd io.proveo.schedule, every 60s",
		"",
	}, "\n")
	if got != want {
		t.Errorf("the listing reads\n%s\nwant\n%s", got, want)
	}
}

func TestAFailedJobIsAnItemAfterTheBlockAndAsksOrNamesItsRetry(t *testing.T) {
	t.Parallel()
	loc := ny(t)
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, loc)
	c := Config{Jobs: map[string]Job{
		"muse-lineup":  {Target: "hermes", PromptFile: "p", At: []string{"mon 18:50"}},
		"muse-waivers": {Target: "hermes", PromptFile: "p", At: []string{"tue 20:00"}},
		"muse-trades":  {Target: "hermes", PromptFile: "p", At: []string{"wed 20:00"}},
	}}
	failed := &Result{Entry: "sun 11:35", Started: time.Date(2026, 9, 27, 15, 35, 0, 0, time.UTC), Outcome: "not-ready",
		Detail: "the agent exited 1 before the budget\nmore", Transcript: "/logs/muse-lineup/20260927-113500.log"}
	last := func(job string) *Result {
		if job == "muse-trades" {
			return &Result{Started: failed.Started, Outcome: "budget"}
		}
		return failed
	}
	running := func(job string) *Live {
		if job == "muse-waivers" {
			return &Live{}
		}
		return nil
	}
	rows := Rows(c, now, running, last)
	if got := FailedJobs(rows); len(got) != 1 || got[0].Job != "muse-lineup" || got[0].Failed.Entry != "sun 11:35" {
		t.Fatalf("failed jobs = %+v, want only muse-lineup (waivers runs again, trades hit its budget)", got)
	}
	l := Listing{Now: now, Home: "/h", Tick: installedTick("/h")}
	out := plainRender(rows, l)
	item := strings.Join([]string{
		"error: muse-lineup — not ready Sun Sep 27 11:35",
		"    the agent exited 1 before the budget",
		"    /logs/muse-lineup/20260927-113500.log",
		"    `proveo schedule retry muse-lineup` runs it again",
		"ok: tick — installed",
	}, "\n")
	if !strings.Contains(out, item) {
		t.Errorf("want the failure after the block, its retry dim, then the tick:\n%s", out)
	}
	lineup := "muse-lineup:  Mon Sep 28 18:50 EDT · in 21h 50m\n              hermes · goal · 45m budget\nmuse-waivers:"
	if strings.Count(out, "proveo schedule retry") != 1 || !strings.Contains(out, lineup) {
		t.Errorf("only the failed, idle job names a retry, and its × says its last run once:\n%s", out)
	}
	if strings.Index(out, "muse-trades:") > strings.Index(out, "error:") {
		t.Errorf("the × follows the whole block:\n%s", out)
	}

	var asked []string
	var b strings.Builder
	p := ui.New(&b)
	p.Plain = true
	l.Retry = func(r Row) {
		asked = append(asked, r.Job)
		p.Askf("retry %s? [y/N]", r.Job)
		b.WriteString("y\n")
	}
	Render(p, rows, l)
	got := b.String()
	if len(asked) != 1 || asked[0] != "muse-lineup" || strings.Contains(got, "proveo schedule retry") {
		t.Errorf("on a terminal the question takes the how-to's place (asked %v):\n%s", asked, got)
	}
	if q, tick := strings.Index(got, "retry muse-lineup? [y/N] y"), strings.Index(got, "ok: tick — installed"); q < strings.Index(got, "error: muse-lineup") || tick < q {
		t.Errorf("the question sits under its × and the tick closes the listing:\n%s", got)
	}
}

// The clock spells the budget as schedule.yml and the config row do; a wait reads ui.Span.
func TestTheRunningClockSpellsTheBudgetAsItsConfigDoes(t *testing.T) {
	t.Parallel()
	loc := ny(t)
	now := time.Date(2026, 10, 4, 20, 44, 0, 0, loc)
	c := Config{Jobs: map[string]Job{"lineup": {Target: "hermes", PromptFile: "p", Budget: "1h30m", At: []string{"tue 21:00"}}}}
	running := func(string) *Live { return &Live{Since: time.Date(2026, 10, 4, 19, 37, 0, 0, loc)} }
	out := plainRender(Rows(c, now, running, nil), Listing{Now: now, Tick: installedTick("")})
	want := "lineup: running since 19:37 EDT · 1h7m of 1h30m\n        Tue Oct 6 21:00 EDT · in 2d 0h\n          hermes · goal · 1h30m budget\n"
	if !strings.Contains(out, want) {
		t.Errorf("render missing\n%s\nin\n%s", want, out)
	}
}

func TestARunningJobReadsRunningAsItsValue(t *testing.T) {
	t.Parallel()
	loc := ny(t)
	now := time.Date(2026, 10, 4, 20, 1, 0, 0, loc)
	c := Config{Jobs: map[string]Job{
		"muse-lineup":        {Target: "hermes", PromptFile: "p", Budget: "36m", At: []string{"mon 18:50"}},
		"muse-lineup-refine": {Target: "hermes", PromptFile: "p", Budget: "36m", At: []string{"mon 19:32"}},
		"muse-trades":        {Target: "hermes", PromptFile: "p", At: []string{"tue 21:00"}},
	}}
	running := func(job string) *Live {
		switch job {
		case "muse-lineup-refine":
			return &Live{Since: time.Date(2026, 10, 4, 19, 37, 7, 0, loc)}
		case "muse-trades":
			return &Live{}
		}
		return nil
	}
	out := plainRender(Rows(c, now, running, nil), Listing{Now: now, Tick: installedTick("")})
	for _, want := range []string{
		"muse-lineup-refine: running since 19:37 EDT · 23m of 36m\n                    Mon Oct 5 19:32 EDT · in 23h 31m\n",
		"muse-trades:        running now\n                    Tue Oct 6 21:00 EDT · in 2d 0h\n",
		"muse-lineup:        Mon Oct 5 18:50 EDT · in 22h 49m\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "proveo attach") || strings.Contains(out, "●") {
		t.Errorf("the running value names no command and draws no dot of its own:\n%s", out)
	}

	late := time.Date(2026, 10, 4, 20, 20, 0, 0, loc)
	if got := runningValue(Row{Since: time.Date(2026, 10, 4, 19, 37, 0, 0, loc), Span: 36 * time.Minute}, late); got != "running since 19:37 EDT · 43m of 36m, past the budget" {
		t.Errorf("over budget: %q", got)
	}

	got := RunningTasks(c, now, running)
	if len(got) != 2 || got[0].Job != "muse-lineup-refine" || got[1].Job != "muse-trades" {
		t.Fatalf("RunningTasks = %+v, want refine then trades, once each", got)
	}
	if got[0].Value != "running since 19:37 EDT · 23m of 36m" || got[0].Summary != "● running since 19:37 EDT · 23m of 36m" || got[1].Value != "running now" {
		t.Errorf("value/summary = %q / %q / %q: the attach list and picker read the listing's value", got[0].Value, got[0].Summary, got[1].Value)
	}
}

func TestTheTickClosesTheListing(t *testing.T) {
	t.Parallel()
	loc := ny(t)
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, loc)
	timed := Rows(Config{Jobs: map[string]Job{"a": {Target: "hermes", PromptFile: "p", At: []string{"mon 18:50"}}}}, now, nil, nil)
	manual := Rows(Config{Jobs: map[string]Job{"a": {Target: "hermes", PromptFile: "p"}}}, now, nil, nil)
	home := filepath.Join("/h", ".proveo")
	for _, c := range []struct {
		name string
		rows []Row
		tick Tick
		want string
	}{
		{"installed", timed, installedTick(home), "ok: tick — installed\n    launchd io.proveo.schedule, every 60s\n"},
		{"missing while jobs fire", timed, Tick{Install: "`proveo schedule install` registers it with launchd"},
			"warn: tick — not installed\n    `proveo schedule install` registers it with launchd\n"},
		{"missing with nothing to fire", manual, Tick{Install: "`proveo schedule install` registers it with launchd"}, "tick — not installed\n"},
		{"missing with no jobs", nil, Tick{Install: "x"}, "tick — not installed\n"},
		{"missing under another home: its install would not serve this one", timed,
			Tick{Install: "`proveo schedule install` registers it with launchd", Home: "/h/other"}, "warn: tick — not installed\n"},
		{"its last run failed", timed, Tick{Installed: true, Home: home, Exit: "1", Log: "/var/log/tick.log"},
			"warn: tick — its last run exited 1\n    /var/log/tick.log\n"},
		{"it reads another home", timed, installedTick("/h/other"), "warn: tick — reads /h/other/schedule.yml, not this home's\n"},
		{"no service manager", timed, Tick{NoManager: "freebsd"},
			"warn: tick — no service manager on freebsd\n    run `proveo schedule tick` every minute yourself\n"},
		{"no service manager and no tmux", timed, Tick{NoManager: "windows"}, "warn: tick — no service manager on windows\n"},
	} {
		out := plainRender(c.rows, Listing{Now: now, Home: home, Tick: c.tick})
		if !strings.HasSuffix(out, c.want) || strings.Count(out, "tick —") != 1 {
			t.Errorf("%s: the listing ends\n%s\nwant it to end on\n%s", c.name, out, c.want)
		}
	}
}

func TestAScheduleOfManualJobsIsNotReadAsEmpty(t *testing.T) {
	t.Parallel()
	loc := ny(t)
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, loc)
	c := Config{Jobs: map[string]Job{
		"probe":  {Command: []string{"true"}, PromptFile: "p", Mode: "plain"},
		"lineup": {Target: "hermes", PromptFile: "p", At: []string{"mon 18:50"}},
	}}
	last := func(job string) *Result {
		if job == "probe" {
			return &Result{Entry: "manual", Started: time.Date(2026, 9, 27, 20, 0, 0, 0, loc), Outcome: "failed", Detail: "exited 2"}
		}
		return nil
	}
	rows := Rows(c, now, nil, last)
	if len(rows) != 2 || rows[0].Job != "lineup" || rows[1].Job != "probe" || !rows[1].Next.IsZero() {
		t.Fatalf("rows = %+v, want lineup's run, then probe with no set time", rows)
	}
	out := plainRender(rows, Listing{Now: now, Tick: Tick{}})
	for _, want := range []string{"probe:  manual\n        command · plain · 45m budget\n", "error: probe — failed Sun Sep 27 20:00\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("a job only `schedule run` starts must list, and its failure with it: missing %q\n%s", want, out)
		}
	}
	if got := FailedJobs(rows); len(got) != 1 || got[0].Job != "probe" {
		t.Errorf("failed jobs = %+v, want probe offered a retry", got)
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
