// SPEC: _spec/internal/schedule/schedule.puml
package schedule

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/ui"
)

// Row is one scheduled time of one job; a job with no `at` entry has one row with a zero Next.
type Row struct {
	Next                                    time.Time
	Job, Entry, Target, Model, Mode, Budget string
	Status, Last                            string
	Since                                   time.Time     // start of the run in progress, in the job's tz; zero when idle or unknown
	Span                                    time.Duration // the job's budget
	Failed                                  *Result       // the last run, started in the job's tz, when it failed and nothing runs now
}

// Live is a run in progress: its tmux session is up.
type Live struct {
	Since time.Time // zero when no transcript names the start
}

// Rows lists every `at` entry of every job, soonest first, then each job only `schedule run` starts.
func Rows(c Config, now time.Time, running func(job string) *Live, last func(job string) *Result) []Row {
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
		var since time.Time
		if running != nil {
			if l := running(name); l != nil {
				status = "running"
				if !l.Since.IsZero() {
					since = l.Since.In(loc)
				}
			}
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
					f := *r
					f.Started = r.Started.In(loc)
					failed = &f
				}
			}
		}
		model := j.Model
		if model == "" {
			model = "-"
		}
		row := Row{
			Job: name, Target: target, Model: model,
			Mode: j.ModeOrDefault(), Budget: compact(budget), Status: status, Last: lastRun, Failed: failed,
			Since: since, Span: budget,
		}
		if len(j.At) == 0 {
			out = append(out, row)
		}
		for _, e := range j.At {
			next, err := NextOccurrence(e, now, loc)
			if err != nil {
				continue
			}
			row.Next, row.Entry = next, e
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, k int) bool {
		a, b := out[i], out[k]
		if a.Next.IsZero() != b.Next.IsZero() {
			return b.Next.IsZero()
		}
		if !a.Next.Equal(b.Next) {
			return a.Next.Before(b.Next)
		}
		return a.Job < b.Job
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
	if d := next.Sub(now); d >= time.Minute {
		return "in " + ui.Span(d)
	}
	return "now"
}

// Tick is the minutely tick as the host's service manager holds it.
type Tick struct {
	Installed bool
	Manager   string // how it runs: "launchd io.proveo.schedule, every 60s"
	Exit      string // the last run's exit status, when it was not 0
	Log       string // the file the tick writes to, as the service manager names it
	Home      string // the proveo home the tick reads, or would read once installed
	Install   string // the how-to that registers it
	NoManager string // the OS, when proveo registers no tick there
}

// Listing is what Render draws around the rows.
type Listing struct {
	Now   time.Time
	Home  string // the proveo home the rows came from
	Tick  Tick
	Retry func(Row) // asks to retry a failed job where its how-to would print; nil prints the how-to
}

// Render draws the schedule: one Facts block keyed by job, soonest first; a × item per job whose last
// run failed; then the tick's closing line.
func Render(p *ui.Printer, rows []Row, l Listing) {
	var order []string
	byJob := map[string][]Row{}
	for _, r := range rows {
		if _, seen := byJob[r.Job]; !seen {
			order = append(order, r.Job)
		}
		byJob[r.Job] = append(byJob[r.Job], r)
	}
	var facts []ui.Fact
	for _, job := range order {
		facts = append(facts, jobFacts(byJob[job], l.Now)...)
	}
	p.Facts(facts...)
	for _, r := range FailedJobs(rows) {
		f := r.Failed
		p.Failf("%s — %s %s", r.Job, strings.ReplaceAll(f.Outcome, "-", " "), f.Started.Format("Mon Jan 2 15:04"))
		if d := firstLine(f.Detail); d != "" {
			p.Detailf("%s", ui.TildeIn(clip(d, 160)))
		}
		if f.Transcript != "" {
			p.Detailf("%s", ui.Tilde(f.Transcript))
		}
		if l.Retry == nil {
			p.Detailf("`proveo schedule retry %s` runs it again", r.Job)
			continue
		}
		l.Retry(r)
	}
	tickLine(p, l, fires(rows))
}

// jobFacts are one job's rows of the block: its soonest run or its running value, each later run
// stacked under it, and the job's configuration and last run dim under the last of them.
func jobFacts(runs []Row, now time.Time) []ui.Fact {
	head := runs[0]
	facts := []ui.Fact{{Role: ui.RoleAsync, Key: head.Job, Value: when(head, now)}}
	later := runs[1:]
	if head.Status == "running" {
		facts[0].Value, later = runningValue(head, now), runs
	}
	for _, r := range later {
		if !r.Next.IsZero() {
			facts = append(facts, ui.Fact{Value: when(r, now)})
		}
	}
	detail := []string{config(head)}
	if head.Failed == nil && head.Last != "never" {
		detail = append(detail, "last "+head.Last)
	}
	facts[len(facts)-1].Detail = detail
	return facts
}

// when is a run's time and the wait to it, or "manual" for a job no `at` entry starts.
func when(r Row, now time.Time) string {
	if r.Next.IsZero() {
		return "manual"
	}
	return r.Next.Format("Mon Jan 2 15:04 MST") + " · " + until(r.Next, now)
}

// config is what a job runs: its target, model, mode and budget.
func config(r Row) string {
	parts := []string{r.Target}
	if r.Model != "-" {
		parts = append(parts, r.Model)
	}
	return strings.Join(append(parts, r.Mode, r.Budget+" budget"), " · ")
}

// fires says whether any row is a set time, which only a registered tick starts.
func fires(rows []Row) bool {
	for _, r := range rows {
		if !r.Next.IsZero() {
			return true
		}
	}
	return false
}

// tickLine is the listing's closing line: whether the tick that fires its jobs is there and serves this home;
// the install how-to prints only when the tick it registers would serve this home.
func tickLine(p *ui.Printer, l Listing, fires bool) {
	t := l.Tick
	otherHome := t.Home != "" && l.Home != "" && filepath.Clean(t.Home) != filepath.Clean(l.Home)
	switch {
	case t.NoManager != "":
		text := "tick — no service manager on " + t.NoManager
		if !fires {
			p.Asyncf("%s", text)
			return
		}
		p.Warnf("%s", text)
		if t.NoManager != "windows" {
			p.Detailf("run `proveo schedule tick` every minute yourself")
		}
	case !t.Installed:
		if !fires {
			p.Asyncf("tick — not installed")
			return
		}
		p.Warnf("tick — not installed")
		if t.Install != "" && !otherHome {
			p.Detailf("%s", t.Install)
		}
	case otherHome:
		p.Warnf("tick — reads %s, not this home's", ui.Tilde(Path(t.Home)))
	case t.Exit != "":
		p.Warnf("tick — its last run exited %s", t.Exit)
		if t.Log != "" {
			p.Detailf("%s", ui.Tilde(t.Log))
		}
	default:
		p.Okf("tick — installed")
		p.Detailf("%s", t.Manager)
	}
}

// runningValue is a run in progress in one line: since when, and the time it took against its budget.
func runningValue(r Row, now time.Time) string {
	if r.Since.IsZero() {
		return "running now"
	}
	elapsed := now.Sub(r.Since).Truncate(time.Minute)
	clock := fmt.Sprintf("%s of %s", compact(elapsed), compact(r.Span))
	if elapsed > r.Span {
		clock += ", past the budget"
	}
	return fmt.Sprintf("running since %s · %s", r.Since.Format("15:04 MST"), clock)
}

// RunningTask is one job whose tmux session is up.
type RunningTask struct {
	Job, Target string
	Value       string // the run in one line, as the schedule listing prints it
	Summary     string // Value behind a dot, for a picker row
}

// RunningTasks lists each running job once, soonest scheduled entry first.
func RunningTasks(c Config, now time.Time, running func(job string) *Live) []RunningTask {
	var out []RunningTask
	seen := map[string]bool{}
	for _, r := range Rows(c, now, running, nil) {
		if r.Status != "running" || seen[r.Job] {
			continue
		}
		seen[r.Job] = true
		v := runningValue(r, now)
		out = append(out, RunningTask{Job: r.Job, Target: r.Target, Value: v, Summary: "● " + v})
	}
	return out
}
