// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (ONE SESSION PER WALK), _spec/internal/choiceui/session.puml
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/ui"
)

// walkView is where init's pages and steps show: one card on a terminal, step lines without one.
type walkView interface {
	Ask(*choiceui.Form) (choiceui.Outcome, error)
	Sub(*choiceui.Form) (choiceui.Outcome, error)
	Plan([]string)
	Status(label string, m choiceui.Mark, outcome string)
	Edit(func(*choiceui.Form))
	Hand(c *exec.Cmd, parts []string, outcome func(error) (choiceui.Mark, string)) error
	HandAs(shown string, c *exec.Cmd, parts []string, outcome func(error) (choiceui.Mark, string)) error
	Dialog(choiceui.Dialog) (int, error)
	Context() context.Context
	card() bool
}

// cardSession is what cardView needs of a choiceui.Session.
type cardSession interface {
	Ask(*choiceui.Form) (choiceui.Outcome, error)
	Resume(*choiceui.Form)
	Plan([]string)
	Status(label string, m choiceui.Mark, outcome string)
	Edit(func(*choiceui.Form))
	Hand(command string, parts []string, run func() error, outcome func(error) (choiceui.Mark, string)) error
	Dialog(choiceui.Dialog) (int, error)
	Context() context.Context
	Close() error
}

// openCard opens the session a prompted walk runs on.
var openCard = func() (cardSession, error) { return choiceui.OpenSession() }

// rowState is a row's mark and outcome as a run left it.
type rowState struct {
	mark    choiceui.Mark
	outcome string
}

// ran reports whether the row's step ran: done, needs you, or failed.
func (s rowState) ran() bool {
	return s.mark == choiceui.MarkDone || s.mark == choiceui.MarkNeeds || s.mark == choiceui.MarkFailed
}

// cardView is a walk on one Session: the page up now, and every row's last outcome.
type cardView struct {
	s      cardSession
	page   *choiceui.Form
	seen   map[string]rowState
	handed bool // a hand-off printed on the primary screen
}

func newCardView(s cardSession) *cardView { return &cardView{s: s, seen: map[string]rowState{}} }

func (v *cardView) Ask(f *choiceui.Form) (choiceui.Outcome, error) {
	v.page = f
	return v.s.Ask(f)
}

// Sub asks f over the running page, then puts the page back up running; after a stop it asks
// nothing and answers Cancelled.
func (v *cardView) Sub(f *choiceui.Form) (choiceui.Outcome, error) {
	if v.s.Context().Err() != nil {
		return choiceui.Cancelled, nil
	}
	out, err := v.s.Ask(f)
	if v.page != nil {
		v.s.Resume(v.page)
	}
	return out, err
}

func (v *cardView) Plan(labels []string) { v.s.Plan(labels) }

func (v *cardView) Status(label string, m choiceui.Mark, outcome string) {
	if m != choiceui.MarkRunning && m != choiceui.MarkAhead {
		v.seen[label] = rowState{mark: m, outcome: outcome}
	}
	v.s.Status(label, m, outcome)
}

func (v *cardView) Edit(edit func(*choiceui.Form)) { v.s.Edit(edit) }

// Hand lends the terminal to c under a frame that names c's argv.
func (v *cardView) Hand(c *exec.Cmd, parts []string, outcome func(error) (choiceui.Mark, string)) error {
	return v.HandAs(commandLine(c), c, parts, outcome)
}

// HandAs lends the terminal to c under a frame that names shown and ends on the page that
// returns; after a stop it lends nothing and returns the stop.
func (v *cardView) HandAs(shown string, c *exec.Cmd, parts []string, outcome func(error) (choiceui.Mark, string)) error {
	if err := v.s.Context().Err(); err != nil {
		return handoffErr{err: err, interrupted: true}
	}
	v.handed = true
	resumes := "init resumes when it exits"
	if v.page != nil {
		resumes = fmt.Sprintf("page %d/%d returns when it exits", v.page.Step.At, v.page.Step.Of)
	}
	return v.s.Hand(shown, append(slices.Clone(parts), resumes), func() error { return handOff(c) },
		func(err error) (choiceui.Mark, string) {
			switch {
			case err != nil:
				return failedMark(err)
			case outcome != nil:
				return outcome(err)
			}
			return choiceui.MarkDone, "done"
		})
}

func (v *cardView) Dialog(d choiceui.Dialog) (int, error) { return v.s.Dialog(d) }

func (v *cardView) Context() context.Context { return v.s.Context() }

func (v *cardView) card() bool { return true }

// errNoCard is what a question gets where no card is up to ask it.
var errNoCard = errors.New("no terminal to ask on")

// streamView is a walk without a card: --yes, or no terminal. Its steps print their lines.
type streamView struct{}

func (streamView) Ask(*choiceui.Form) (choiceui.Outcome, error) { return choiceui.Cancelled, errNoCard }
func (streamView) Sub(*choiceui.Form) (choiceui.Outcome, error) { return choiceui.Cancelled, errNoCard }
func (streamView) Plan([]string)                                {}
func (streamView) Edit(func(*choiceui.Form))                    {}
func (streamView) Dialog(choiceui.Dialog) (int, error)          { return 0, errNoCard }
func (streamView) Context() context.Context                     { return context.Background() }
func (streamView) card() bool                                   { return false }

// Status prints a running row as its live line.
func (streamView) Status(label string, m choiceui.Mark, outcome string) {
	if m == choiceui.MarkRunning && outcome != "" {
		ui.Asyncf("%s — %s", label, outcome)
	}
}

// Hand gives the terminal to c under its frame line.
func (v streamView) Hand(c *exec.Cmd, parts []string, outcome func(error) (choiceui.Mark, string)) error {
	return v.HandAs(commandLine(c), c, parts, outcome)
}

// HandAs gives the terminal to c under a frame line that names shown.
func (streamView) HandAs(shown string, c *exec.Cmd, parts []string, _ func(error) (choiceui.Mark, string)) error {
	return handFramedAs(shown, c, append(slices.Clone(parts), "init resumes when it exits")...)
}

// viewOr is v, or the stream when there is none.
func viewOr(v walkView) walkView {
	if v == nil {
		return streamView{}
	}
	return v
}

// commandLine is c's argv as its frame names it: the program by its base name.
func commandLine(c *exec.Cmd) string {
	return strings.Join(append([]string{filepath.Base(c.Args[0])}, c.Args[1:]...), " ")
}

// runCaptured runs c with its output kept off the terminal; a failure carries the output's last lines.
func runCaptured(c *exec.Cmd) error {
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	if err == nil {
		return nil
	}
	if tail := lastLines(out.String(), 3); tail != "" {
		return fmt.Errorf("%w — %s", err, tail)
	}
	return err
}

// runArgvCaptured is runCaptured for argv.
func runArgvCaptured(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("no command")
	}
	return runCaptured(exec.Command(argv[0], argv[1:]...))
}

// lastLines is text's last n non-blank lines joined by " — ".
func lastLines(text string, n int) string {
	var lines []string
	for l := range strings.SplitSeq(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " — ")
}
