// SPEC: _spec/internal/schedule/schedule.puml
package schedule

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/ui"
)

// Row is one scheduled time of one job.
type Row struct {
	Next                                    time.Time
	Job, Entry, Target, Model, Mode, Budget string
	Status, Last                            string
	Failed                                  *Result // the last run, when it failed and nothing runs now
}

// Rows lists every `at` entry of every job, soonest first.
func Rows(c Config, now time.Time, running func(job string) bool, last func(job string) *Result) []Row {
	var out []Row
	for name, j := range c.Jobs {
		loc, err := j.Location()
		if err != nil {
			continue
		}
		budget, _ := j.BudgetDuration()
		target := j.Target
		if len(j.Command) > 0 {
			target = "command"
		}
		status := "idle"
		if running != nil && running(name) {
			status = "running"
		}
		lastRun := "never"
		var failed *Result
		if last != nil {
			if r := last(name); r != nil {
				lastRun = r.Started.In(loc).Format("Mon Jan 2 15:04") + " " + r.Outcome
				switch {
				case r.Summary != "":
					lastRun += " · " + clip(r.Summary, 80)
				case r.Outcome == "ended" || r.Outcome == "budget":
					lastRun += " · no report"
				}
				if r.Failed() && status != "running" {
					failed = r
				}
			}
		}
		for _, e := range j.At {
			next, err := NextOccurrence(e, now, loc)
			if err != nil {
				continue
			}
			model := j.Model
			if model == "" {
				model = "-"
			}
			out = append(out, Row{
				Next: next, Job: name, Entry: e, Target: target, Model: model,
				Mode: j.ModeOrDefault(), Budget: compact(budget), Status: status, Last: lastRun, Failed: failed,
			})
		}
	}
	sort.Slice(out, func(i, k int) bool {
		if !out[i].Next.Equal(out[k].Next) {
			return out[i].Next.Before(out[k].Next)
		}
		return out[i].Job < out[k].Job
	})
	return out
}

// FailedJobs names each job whose last run failed, with that run, in listing order.
func FailedJobs(rows []Row) []Row {
	var out []Row
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Failed != nil && !seen[r.Job] {
			seen[r.Job] = true
			out = append(out, r)
		}
	}
	return out
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// compact renders a duration as 45m, 1h or 1h30m.
func compact(d time.Duration) string {
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dm", m)
}

// until renders the wait to next as "in 1d 2h" / "in 45m".
func until(next, now time.Time) string {
	d := next.Sub(now)
	if d < time.Minute {
		return "now"
	}
	d = d.Round(time.Minute)
	days, rest := int(d/(24*time.Hour)), d%(24*time.Hour)
	h, m := int(rest/time.Hour), int(rest%time.Hour/time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("in %dd %dh", days, h)
	case h > 0:
		return fmt.Sprintf("in %dh %dm", h, m)
	}
	return fmt.Sprintf("in %dm", m)
}

// Tick is whether the host's service manager holds the minutely tick.
type Tick struct {
	Installed bool
	Detail    string
}

// Render draws the schedule in proveo's status vocabulary: a section per job, its runs soonest first.
func Render(p *ui.Printer, rows []Row, now time.Time, tick Tick, logDir func(job string) string) {
	p.Section(ui.SectionSchedule)
	if tick.Installed {
		p.Okf("tick installed: %s", tick.Detail)
	} else {
		p.Warnf("tick not installed: %s", tick.Detail)
	}
	if len(rows) == 0 {
		p.Notef("nothing scheduled — add jobs to ~/.proveo/%s", FileName)
		return
	}
	paint := func(color int, bold bool, s string) string {
		if p.Plain {
			return s
		}
		pre := ui.ANSI(color)
		if bold {
			pre += ui.ANSIBold
		}
		return pre + s + ui.ANSIReset
	}
	next := "▸"
	if p.Tier != ui.GlyphsNerd {
		next = ">"
	}
	var order []string
	byJob := map[string][]Row{}
	for _, r := range rows {
		if _, seen := byJob[r.Job]; !seen {
			order = append(order, r.Job)
		}
		byJob[r.Job] = append(byJob[r.Job], r)
	}
	for _, job := range order {
		runs := byJob[job]
		head := runs[0]
		p.Section(job)
		p.Asyncf("%s · %s · %s · %s budget · %s · last %s", head.Target, head.Model, head.Mode, head.Budget,
			head.Status, head.Last)
		if f := head.Failed; f != nil {
			p.Failf("%s: %s — `proveo schedule retry %s`", f.Outcome, clip(firstLine(f.Detail), 160), job)
		}
		for _, r := range runs {
			when := fmt.Sprintf("%-20s  %-10s  %s", r.Next.Format("Mon Jan 2 15:04 MST"), until(r.Next, now), r.Entry)
			if r.Next.Equal(rows[0].Next) && r.Job == rows[0].Job {
				p.Notef("%s %s", paint(ui.ColorBrand, true, next), paint(ui.ColorBrand, true, when))
				continue
			}
			p.Notef("  %s", paint(ui.ColorSecondary, false, when))
		}
		if logDir != nil {
			p.Storef("transcripts %s", logDir(job))
		}
	}
}
