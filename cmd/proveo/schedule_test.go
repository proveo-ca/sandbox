// SPEC: _spec/internal/schedule/schedule.puml
package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/schedule"
)

func TestOfferRetriesRelaunchesEachYes(t *testing.T) {
	t.Parallel()
	failed := []schedule.Row{
		{Job: "muse-lineup", Failed: &schedule.Result{Entry: "sun 11:35", Outcome: "failed"}},
		{Job: "muse-waivers", Failed: &schedule.Result{Entry: "tue 20:00", Outcome: "not-ready"}},
		{Job: "muse-trades", Failed: &schedule.Result{Entry: "wed 20:00", Outcome: "failed"}},
	}
	var retried []string
	var out strings.Builder
	err := offerRetries(failed, strings.NewReader("y\n\nyes\n"), &out, func(job string) error {
		retried = append(retried, job)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"muse-lineup", "muse-trades"}; !slices.Equal(retried, want) {
		t.Errorf("retried %v, want %v (Enter means no)", retried, want)
	}
	if !strings.Contains(out.String(), "retry muse-waivers (tue 20:00)? [y/N]") {
		t.Errorf("prompt names the job and its entry:\n%s", out.String())
	}
}

func TestRetryJobRefusesARunThatDidNotFail(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	c := schedule.Config{Jobs: map[string]schedule.Job{"lineup": {Target: "hermes", PromptFile: "p", At: []string{"sun 11:35"}}}}
	if err := retryJob(home, c, "lineup"); err == nil || !strings.Contains(err.Error(), "has not run yet") {
		t.Errorf("never-run job: %v", err)
	}
	dir := schedule.LogDir(home, "lineup")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20260927-113500.json"), []byte(`{"outcome":"budget"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := retryJob(home, c, "lineup"); err == nil || !strings.Contains(err.Error(), "last run budget, not failed") {
		t.Errorf("budget run: %v", err)
	}
	if err := retryJob(home, c, "nope"); err == nil || !strings.Contains(err.Error(), `no job "nope"`) {
		t.Errorf("unknown job: %v", err)
	}
}

func TestAttachArgsPerTerminal(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		readOnly, inTmux bool
		want             string
		nested           bool
	}{
		{false, false, "attach -t s", false},
		{true, false, "attach -r -t s", false},
		{false, true, "switch-client -t s", false},
		{true, true, "attach -r -t s", true},
	} {
		args, nested := attachArgs("s", c.readOnly, c.inTmux)
		if got := strings.Join(args, " "); got != c.want || nested != c.nested {
			t.Errorf("readOnly=%v inTmux=%v: %q nested=%v, want %q nested=%v", c.readOnly, c.inTmux, got, nested, c.want, c.nested)
		}
	}
}

func TestScheduleHelpExampleLoads(t *testing.T) {
	t.Parallel()
	long := scheduleCmd().Long
	start := strings.Index(long, "  jobs:")
	end := strings.Index(long[start:], "\n\n")
	if start < 0 || end < 0 {
		t.Fatalf("no schedule.yml example in:\n%s", long)
	}
	var yml strings.Builder
	for _, l := range strings.Split(long[start:start+end], "\n") {
		yml.WriteString(strings.TrimPrefix(l, "  ") + "\n")
	}
	home := t.TempDir()
	if err := os.WriteFile(schedule.Path(home), []byte(yml.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := schedule.Load(home)
	if err != nil {
		t.Fatalf("the help's example must be a valid schedule.yml: %v\n%s", err, yml.String())
	}
	if j := c.Jobs["sample-task"]; len(j.At) != 2 || j.Target != "hermes" {
		t.Errorf("parsed %+v", j)
	}
}
