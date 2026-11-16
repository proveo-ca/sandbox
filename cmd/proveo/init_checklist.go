// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (FOUR PAGES ON ONE CARD), _spec/internal/choiceui/wireframe.puml (THE CHECKLIST CARD)
package main

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/posture"
)

// initPages is how many pages a prompted init walks.
const initPages = 4

// The pages' short names: the done row, the ahead row and the result name a page by them.
const (
	pageSbx         = "sbx"
	pageGit         = "git, shell"
	pageCredentials = "credentials"
	pageOperator    = "operator"
)

// pageNames are the short names in page order.
var pageNames = [initPages]string{pageSbx, pageGit, pageCredentials, pageOperator}

// pageAsks is what each page asks, as its ahead row says.
var pageAsks = [initPages]string{
	"",
	"git identity, GitHub, prompt kit",
	"agent sign-ins and API keys sbx holds",
	"the name agents address you by",
}

// The card's border label, the checks section and the ahead section.
const (
	initBorder   = "proveo init"
	rowChecks    = "checks"
	aheadHeading = "ahead"
)

// The sub-pages' titles.
const (
	titleAccess = "Choose sandbox Git access"
	titleRepair = "Fix GitHub SSH for sandboxes"
)

// initStepAt is page n of the walk, with the quit question that names it.
func initStepAt(n int) choiceui.Step {
	return choiceui.Step{At: n, Of: initPages, Leave: choiceui.Leave{
		Title: "Quit init?",
		Body:  fmt.Sprintf("Answers on page %d/%d are not saved.\nSteps that ran stay done.", n, initPages),
	}}
}

// initCard is an init page as a checklist card: the border label, the page's chip and quit
// question, and the terminal's glyph tier.
func initCard(at choiceui.Step, title string, header []string, rows []choiceui.Row) *choiceui.Form {
	return &choiceui.Form{
		Border:    initBorder,
		Checklist: true,
		Title:     title,
		Header:    header,
		Rows:      rows,
		Step:      at,
		Glyphs:    posture.GlyphModeFrom(os.Getenv),
	}
}

// runWord is what Enter does when n rows of the page will run.
func runWord(n int) string {
	switch n {
	case 0:
		return "next"
	case 1:
		return "run 1 step"
	}
	return fmt.Sprintf("run %d steps", n)
}

// planRows are the card rows a plan runs, in order, each once: a listed step's row.
func planRows(plan []applyStep) []string {
	var out []string
	for _, s := range plan {
		if r := s.cardRow(); r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// aheadRows are the ahead section from page from on: one row per page not reached, then sbx setup.
func aheadRows(from int, setup choiceui.Row) []choiceui.Row {
	var rows []choiceui.Row
	for n := from; n <= initPages; n++ {
		rows = append(rows, choiceui.Row{Kind: choiceui.RowAhead, Mark: choiceui.MarkAhead, Label: pageNames[n-1],
			Outcome: fmt.Sprintf("page %d/%d · %s", n, initPages, pageAsks[n-1])})
	}
	rows = append(rows, setup)
	rows[0].Divider, rows[0].Heading = true, aheadHeading
	return rows
}

// setupAhead is the sbx setup row before page 4: it waits for the result, or for later under --skip-setup.
func setupAhead(skip bool) choiceui.Row {
	if skip {
		return choiceui.Row{Kind: choiceui.RowStatus, Mark: choiceui.MarkLater, Label: rowWizard, Outcome: "later · --skip-setup"}
	}
	return choiceui.Row{Kind: choiceui.RowStatus, Mark: choiceui.MarkAhead, Label: rowWizard, Outcome: "after init, while sbx's store lacks an env key"}
}

// pageDone is what a page left: its done row's mark, outcome and count form.
type pageDone struct {
	mark           choiceui.Mark
	outcome, brief string
}

// row is the page's done row under its short name.
func (d pageDone) row(name string) choiceui.Row {
	return choiceui.Row{Kind: choiceui.RowDone, Mark: d.mark, Label: name, Outcome: d.outcome, Brief: d.brief}
}

// nothingToAsk is the done row of a page that had nothing to ask.
var nothingToAsk = pageDone{mark: choiceui.MarkLater, outcome: "nothing to ask"}

// doneOf sums a page's rows into its done row: the worst mark, and the outcomes in row order.
func doneOf(states []labelled, extra string, brief func(later int) string) pageDone {
	var marks []choiceui.Mark
	var parts []string
	later := 0
	for _, s := range states {
		marks = append(marks, s.mark)
		if s.label == rowChecks {
			continue
		}
		parts = append(parts, s.doneText())
		if s.mark == choiceui.MarkLater {
			later++
		}
	}
	if extra != "" {
		parts = append(parts, extra)
	}
	d := pageDone{mark: choiceui.Worst(marks...), outcome: strings.Join(parts, " · ")}
	if d.mark == choiceui.MarkNone {
		d.mark = choiceui.MarkLater
	}
	if brief != nil {
		d.brief = brief(later)
	}
	return d
}

// labelled is a row's state under its label.
type labelled struct {
	label string
	rowState
}

// doneText is how a done row names the row's outcome: Docker's sign-in by Docker, a row left for
// later by its label.
func (s labelled) doneText() string {
	switch {
	case s.label == rowLogin:
		return "Docker " + s.outcome
	case s.mark == choiceui.MarkLater && !strings.HasPrefix(s.outcome, "kept"):
		return s.label + " " + s.outcome
	}
	return s.outcome
}

// needYou counts the checks still marked as needing the operator: "n checks need you".
func needYou(rows []choiceui.Row) string {
	n := 0
	for _, r := range rows {
		if r.Kind == choiceui.RowCheck && (r.Mark == choiceui.MarkNeeds || r.Mark == choiceui.MarkFailed) {
			n++
		}
	}
	switch n {
	case 0:
		return ""
	case 1:
		return "1 check needs you"
	}
	return fmt.Sprintf("%d checks need you", n)
}

// lockRan turns each row whose step ran into its status row: a revisit shows what ran and runs it never again.
func lockRan(rows []choiceui.Row, seen map[string]rowState) []choiceui.Row {
	for i := range rows {
		r := &rows[i]
		st, ok := seen[r.Label]
		if !ok || !st.ran() || r.Kind != choiceui.RowAnswer {
			continue
		}
		r.Kind, r.Mark, r.Outcome = choiceui.RowStatus, st.mark, st.outcome
		r.Options, r.Selected, r.Field, r.When = nil, 0, false, nil
	}
	return rows
}

// subPage is a sub-page of page at: page at's done and ahead rows around the sub-page's own, its
// chip and quit question, and Esc leaves it for later.
func subPage(at choiceui.Step, title string, header []string, rows, done, ahead []choiceui.Row) *choiceui.Form {
	all := slices.Concat(done, rows, ahead)
	f := initCard(at, title, header, all)
	f.Esc = "later"
	return f
}
