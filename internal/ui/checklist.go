// SPEC: _spec/internal/ui/output-vocabulary.puml (THE CLOSED CHECKLIST), _spec/cmd/proveo/init-preflight-and-result.puml
package ui

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

// Check is one line of a closed checklist: its mark, what it names, what it came to, and its detail.
type Check struct {
	Mark    string // the mark as this printer's tier draws it
	Tag     string // the mark's head in plain output: "ok: ", "warn: ", "error: ", or ""
	Ink     int    // the mark's colour
	Label   string
	Outcome string
	Detail  []string
}

// The closed checklist's columns.
const (
	checkMarkCol    = 2
	checkLabelCol   = 4
	checkDetailCol  = 6
	checkOutcomeCol = 18
)

// CheckRow prints c as a row: the mark at 2, the label at 4, the outcome at 18 wrapping at " · "
// back to 18, the detail at 6. Plain output reads "<tag><label> — <outcome>", the detail indented 4.
func (p *Printer) CheckRow(c Check) {
	if p.Plain {
		line := c.Tag + c.Label
		if c.Outcome != "" {
			line += " — " + c.Outcome
		}
		p.checkLines(line+"\n", c.Detail)
		return
	}
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", checkMarkCol) + c.mark() + " " + c.Label)
	if c.Outcome == "" {
		p.checkLines(b.String()+"\n", c.Detail)
		return
	}
	pad := max(checkOutcomeCol-checkLabelCol-textWidthOf(c.Label), 2)
	for i, l := range wrapSegments(c.Outcome, p.room(checkOutcomeCol)) {
		if i == 0 {
			b.WriteString(strings.Repeat(" ", pad) + l + "\n")
			continue
		}
		b.WriteString(strings.Repeat(" ", checkOutcomeCol) + l + "\n")
	}
	p.checkLines(b.String(), c.Detail)
}

// CheckItem prints c as an item: the mark at 2, Label as its text from 4 wrapping back to 6, the
// detail at 6; bold sets the text in bold, as a verdict reads. Plain output reads "<tag><label>".
func (p *Printer) CheckItem(c Check, bold bool) {
	if p.Plain {
		p.checkLines(c.Tag+c.Label+"\n", c.Detail)
		return
	}
	var b strings.Builder
	for i, l := range wrapWords(c.Label, p.room(checkLabelCol), p.room(checkDetailCol)) {
		if bold {
			l = ANSIBold + l + ANSIReset
		}
		if i == 0 {
			b.WriteString(strings.Repeat(" ", checkMarkCol) + c.mark() + " " + l + "\n")
			continue
		}
		b.WriteString(strings.Repeat(" ", checkDetailCol) + l + "\n")
	}
	p.checkLines(b.String(), c.Detail)
}

// mark is the mark in its ink; a zero Ink leaves it plain.
func (c Check) mark() string {
	if c.Ink == 0 {
		return c.Mark
	}
	return tint(c.Ink, c.Mark)
}

// checkLines writes head, then each detail line: at 6 and dim on a terminal, indented 4 in plain output.
func (p *Printer) checkLines(head string, detail []string) {
	var b strings.Builder
	b.WriteString(head)
	for _, d := range detail {
		if p.Plain {
			b.WriteString(strings.Repeat(" ", textCol+2) + d + "\n")
			continue
		}
		for _, l := range wrapWords(d, p.room(checkDetailCol), p.room(checkDetailCol)) {
			b.WriteString(strings.Repeat(" ", checkDetailCol) + tint(ColorMuted, l) + "\n")
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.eraseLive()
	defer p.drawLive()
	p.flushSection()
	p.spoke = true
	fmt.Fprint(p.W, b.String())
}

// checkMargin is how many columns a closed checklist leaves blank at the right.
const checkMargin = 2

// room is how many columns a line that starts at col has; 0 when nothing measures the width.
func (p *Printer) room(col int) int {
	w := p.width()
	if w <= 0 {
		return 0
	}
	return max(w-col-checkMargin, 1)
}

// wrapSegments lays outcome into lines of at most room columns, breaking at " · " and dropping the
// separator at the break; a segment wider than room wraps at its spaces. room 0 keeps one line.
func wrapSegments(outcome string, room int) []string {
	if room <= 0 || textWidthOf(outcome) <= room {
		return []string{outcome}
	}
	var lines []string
	cur := ""
	for seg := range strings.SplitSeq(outcome, " · ") {
		switch {
		case cur == "":
		case textWidthOf(cur+" · "+seg) <= room:
			cur += " · " + seg
			continue
		default:
			lines = append(lines, cur)
		}
		parts := wrapWords(seg, room, room)
		lines = append(lines, parts[:len(parts)-1]...)
		cur = parts[len(parts)-1]
	}
	return append(lines, cur)
}

// wrapWords lays s into lines of first columns, then rest columns, breaking at spaces; a word wider
// than its line keeps its head and tail around "…". A room of 0 keeps one line.
func wrapWords(s string, first, rest int) []string {
	if first <= 0 || textWidthOf(s) <= first {
		return []string{s}
	}
	var lines []string
	cur, room := "", first
	for word := range strings.FieldsSeq(s) {
		switch {
		case cur == "":
		case textWidthOf(cur+" "+word) <= room:
			cur += " " + word
			continue
		default:
			lines = append(lines, cur)
			room = rest
		}
		cur = clipMiddle(word, room)
	}
	return append(lines, cur)
}

// clipMiddle is s cut to room columns, its head and tail kept around "…".
func clipMiddle(s string, room int) string {
	if textWidthOf(s) <= room || room < 3 {
		return s
	}
	keep := room - 1
	tail := keep / 2
	head := runewidth.Truncate(s, keep-tail, "")
	rs := []rune(s)
	end, w := len(rs), 0
	for end > 0 && w+runewidth.RuneWidth(rs[end-1]) <= tail {
		end--
		w += runewidth.RuneWidth(rs[end])
	}
	return head + "…" + string(rs[end:])
}
