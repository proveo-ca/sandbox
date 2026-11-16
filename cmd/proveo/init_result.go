// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml (THE CLOSED CHECKLIST)
package main

import (
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/ui"
)

// closedRow is one row of the closed checklist: its mark, its label, what it came to, and the
// lines that say what it still needs.
type closedRow struct {
	mark    choiceui.Mark
	label   string
	outcome string
	detail  []string
}

// stepRows are the rows pages 1 and 2 give the result, in run order.
var stepRows = []string{rowInstall, rowPath, rowLogin, rowBaseline, rowGit, rowGh, rowPrompt}

// checklist is the closed checklist's rows in run order, and the items that need the operator:
// the checks still marked !, then what a step said that no row restates.
func (r *initReport) checklist(rec *initRecord, turn setupTurn) ([]closedRow, []sandbox.Item) {
	readiness := slices.Clone(r.readiness)
	if !r.judged {
		readiness, _ = checkItems(r.diag)
	}
	if r.loginWarned {
		readiness = slices.DeleteFunc(readiness, namesLogin)
	}
	var rows []closedRow
	var said []sandbox.Item
	for _, label := range stepRows {
		own := r.itemsAbout(label)
		row, ok := r.stepRow(rec, label, own)
		if !ok {
			said = append(said, own...)
			continue
		}
		for _, it := range own {
			switch {
			case it.Ok:
				continue
			case restates(it, row):
				row.detail = joinNew(row.detail, it.Detail)
			default:
				said = append(said, it)
			}
		}
		readiness = slices.DeleteFunc(readiness, func(it sandbox.Item) bool {
			subject, _, _ := strings.Cut(it.Text, " — ")
			if !strings.EqualFold(subject, label) {
				return false
			}
			row.detail = joinNew(row.detail, it.Detail)
			return true
		})
		rows = append(rows, row)
	}
	notRun := func(label string) []closedRow {
		if slices.Contains(rec.notRun, label) {
			return []closedRow{{mark: choiceui.MarkLater, label: label, outcome: "not run"}}
		}
		return nil
	}
	switch {
	case r.creds != nil:
		creds := r.creds.rows()
		for _, it := range r.creds.items {
			if !raise(creds, it) {
				said = append(said, it)
			}
		}
		rows = append(rows, creds...)
	default:
		rows = append(rows, notRun(labelCredentials)...)
	}
	switch {
	case r.operator != nil:
		rows = append(rows, r.operator.row())
	default:
		rows = append(rows, notRun(labelOperator)...)
	}
	if turn.row != nil {
		rows = append(rows, *turn.row)
	}
	for i := range rows {
		rows[i].outcome, rows[i].detail = ui.TildeIn(rows[i].outcome), slices.Clone(rows[i].detail)
		for j := range rows[i].detail {
			rows[i].detail[j] = ui.TildeIn(rows[i].detail[j])
		}
	}
	needs := slices.Concat(prereqItems(r.prereqs), readiness, versionItems(r.installed), r.shadowItems(), said, r.opItems)
	needs = slices.DeleteFunc(needs, func(it sandbox.Item) bool { return it.Ok || it.Text == "" })
	return rows, sandbox.WithPaths(needs, ui.TildeIn)
}

// stepRow is row label as the run left it: what its step said, else not run, later, or what the
// host already held; ok is false for a row the walk never showed.
func (r *initReport) stepRow(rec *initRecord, label string, own []sandbox.Item) (closedRow, bool) {
	row := closedRow{label: label}
	later, waits := rec.later[label]
	st, ran := rec.rows[label]
	switch {
	case ran:
		row.mark, row.outcome = st.mark, st.outcome
	case slices.Contains(rec.notRun, label):
		row.mark, row.outcome = choiceui.MarkLater, "not run"
	case waits:
		row.mark, row.outcome, row.detail = choiceui.MarkLater, joinBrief("later", r.flags[label]), later
	case label == rowInstall && r.installed != "":
		row.mark, row.outcome = choiceui.MarkLater, "kept "+r.installed
	case label == rowLogin && r.diagnosed:
		row.mark, row.outcome = authMark(r.auth)
	default:
		at := slices.IndexFunc(own, func(it sandbox.Item) bool { return !it.Ok && it.Text != "" })
		if at < 0 {
			return row, false
		}
		row.mark, row.outcome = itemMark(own[at])
	}
	if label == rowInstall && (row.mark == choiceui.MarkDone || row.mark == choiceui.MarkLater && strings.HasPrefix(row.outcome, "kept")) {
		if at := sbxAt(r.applied); at != "" {
			row.outcome = joinBrief(row.outcome, r.path(at))
		}
		if row.mark == choiceui.MarkDone && r.unverified {
			row.outcome += " · no published digest"
		}
	}
	return row, true
}

// restates reports whether it says what row already says: its subject is the row, or its words
// after " — " are part of the row's outcome.
func restates(it sandbox.Item, row closedRow) bool {
	subject, tail, cut := strings.Cut(it.Text, " — ")
	return strings.EqualFold(subject, row.label) || cut && strings.Contains(row.outcome, tail)
}

// raise joins it to the first of rows its subject names: the row takes the worse mark, its words
// and its detail. It reports false when no row is its subject.
func raise(rows []closedRow, it sandbox.Item) bool {
	subject, _, _ := strings.Cut(it.Text, " — ")
	at := slices.IndexFunc(rows, func(row closedRow) bool { return strings.EqualFold(row.label, subject) })
	if at < 0 || it.Ok {
		return false
	}
	mark, words := itemMark(it)
	row := &rows[at]
	row.mark, row.outcome, row.detail = choiceui.Worst(row.mark, mark), joinBrief(row.outcome, words), joinNew(row.detail, it.Detail)
	return true
}

// joinNew is lines with each of more that it does not hold yet.
func joinNew(lines, more []string) []string {
	for _, l := range more {
		if !slices.Contains(lines, l) {
			lines = append(lines, l)
		}
	}
	return lines
}

// check is row as ui draws it, in this terminal's glyph tier.
func (row closedRow) check() ui.Check {
	return ui.Check{Mark: row.mark.Glyph(ui.Default.Tier, 0), Tag: row.mark.Tag(), Ink: markInk(row.mark),
		Label: row.label, Outcome: row.outcome, Detail: row.detail}
}

// markInk is a mark's colour in the stream.
func markInk(m choiceui.Mark) int {
	switch m {
	case choiceui.MarkDone:
		return ui.ColorSuccess
	case choiceui.MarkNeeds:
		return ui.ColorWarn
	case choiceui.MarkFailed:
		return ui.ColorFailure
	}
	return ui.ColorMuted
}

// itemRow is a needs-you item as a closed checklist line: its mark, its text, its detail.
func itemRow(it sandbox.Item) closedRow {
	m := choiceui.MarkLater
	switch {
	case it.Fail:
		m = choiceui.MarkFailed
	case it.Warn:
		m = choiceui.MarkNeeds
	case it.Ok:
		m = choiceui.MarkDone
	}
	return closedRow{mark: m, label: it.Text, detail: it.Detail}
}

// printRow prints row with its mark at 2, its label at 4 and its outcome at 18.
func printRow(row closedRow) { ui.Default.CheckRow(row.check()) }

// printNeeds prints each item that needs the operator after a blank row.
func printNeeds(items []sandbox.Item) {
	for _, it := range items {
		ui.Blank()
		ui.Default.CheckItem(itemRow(it).check(), false)
	}
}

// printEnding prints the closing line, its text in bold.
func printEnding(row closedRow) { ui.Default.CheckItem(row.check(), true) }
