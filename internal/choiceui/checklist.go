// SPEC: _spec/internal/choiceui/wireframe.puml
package choiceui

import (
	"fmt"
	"slices"
	"strings"
)

// The checklist card's columns inside the walls.
const (
	markCol    = bodyIndent // a row's mark and the cursor
	labelCol   = 4          // labels, check text, facts and prose
	detailCol  = 6          // a check's evidence, fix and note
	outcomeCol = optOrigin  // a status row's outcome; the grid's column 0
	helpMost   = 2          // the most lines the help under a checklist holds
)

// detail is what a check's detail line says.
type detail int

const (
	detailEvidence detail = iota
	detailFix
	detailNote
)

// detailText is one detail entry of a check.
type detailText struct {
	role detail
	text string
}

// details are the check's entries in the order they draw: evidence, fix, note.
func (r *Row) details() []detailText {
	var out []detailText
	for _, e := range r.Evidence {
		if e != "" {
			out = append(out, detailText{detailEvidence, e})
		}
	}
	if r.Fix != "" {
		out = append(out, detailText{detailFix, r.Fix})
	}
	if r.Note != "" {
		out = append(out, detailText{detailNote, r.Note})
	}
	return out
}

// keptDetail is the index in details of the entry the ladder never drops: the fix, else the first
// evidence line; -1 when the check has neither.
func (r *Row) keptDetail() int {
	ds := r.details()
	for i, d := range ds {
		if d.role == detailFix {
			return i
		}
	}
	if len(ds) > 0 && ds[0].role == detailEvidence {
		return 0
	}
	return -1
}

// checkText is a check's line before it wraps: "name — message".
func (r *Row) checkText() string {
	if r.Outcome == "" {
		return r.Label
	}
	return r.Label + " — " + r.Outcome
}

// cut is how far down its ladder a checklist went to fit: what it dropped or folded.
// SPEC: _spec/internal/choiceui/wireframe.puml (THE CHECKLIST CARD)
type cut struct {
	blanks  int  // rung 1: spacing blanks dropped, counted from the bottom
	under   bool // rung 2: the blank row under the card
	prose   bool // rung 3: the prose header line
	passed  bool // rung 4: passed checks fold into one row per section
	details int  // rung 5: detail entries past each check's kept one, counted from the bottom
	done    bool // rung 6: done rows fold into one row
	head    bool // rung 7: the head's blank row
	ahead   bool // rung 8: ahead page rows fold into one row
}

// countShown is how many drawn rows are of kind k.
func (f *Form) countShown(k RowKind) int {
	n := 0
	for i := range f.Rows {
		if f.Rows[i].Kind == k && f.shown(i) {
			n++
		}
	}
	return n
}

// passedIn is how many drawn passed checks section g holds.
func (f *Form) passedIn(groups []int, g int) int {
	n := 0
	for i := range f.Rows {
		if groups[i] == g && f.passed(i) {
			n++
		}
	}
	return n
}

// passed reports whether row i is a drawn check that passed.
func (f *Form) passed(i int) bool {
	return f.Rows[i].Kind == RowCheck && f.Rows[i].Mark == MarkDone && f.shown(i)
}

// foldsPassed reports whether some section holds two passed checks to fold.
func (f *Form) foldsPassed() bool {
	groups := f.groups()
	for i := range f.Rows {
		if f.passed(i) && f.passedIn(groups, groups[i]) > 1 {
			return true
		}
	}
	return false
}

// checklistLines is the checklist's body on a w-column terminal after cut c: done rows, sections
// and their stubs, the spacing blanks, and each check's wrapped text and detail lines.
// SPEC: _spec/internal/choiceui/wireframe.puml (THE CHECKLIST CARD)
func (f *Form) checklistLines(w int, c cut) []bodyLine {
	room := edge(w)
	groups := f.groups()
	var out []bodyLine
	blanks, entries := 0, 0
	blank := func() {
		out = append(out, bodyLine{row: -1, kind: lineBlank, drop: blanks})
		blanks++
	}
	const (
		atTop = iota
		afterDone
		afterBlank
		afterLine
	)
	state, checkBlank := atTop, false
	doneFolded, aheadFolded, passedFolded := false, false, map[int]bool{}
	for i := range f.Rows {
		if !f.shown(i) {
			continue
		}
		r := &f.Rows[i]
		if r.Kind == RowDone {
			switch {
			case !c.done:
				out = append(out, bodyLine{row: i, kind: lineRow, drop: -1})
			case !doneFolded:
				out, doneFolded = append(out, f.doneFold()), true
			}
			state = afterDone
			continue
		}
		if state == afterDone {
			blank()
			state = afterBlank
		}
		switch {
		case r.Divider:
			if state == afterLine {
				blank()
			}
			out = append(out, bodyLine{row: i, kind: lineHeading, drop: -1})
		case checkBlank && r.Kind == RowCheck:
			blank()
		}
		state, checkBlank = afterLine, false
		switch {
		case r.Kind == RowAhead && c.ahead:
			if !aheadFolded {
				out, aheadFolded = append(out, f.aheadFold()), true
			}
		case f.passed(i) && c.passed && f.passedIn(groups, groups[i]) > 1:
			if !passedFolded[groups[i]] {
				out, passedFolded[groups[i]] = append(out, f.passedFold(groups, groups[i])), true
			}
		case r.Kind == RowCheck:
			lines := wrap(r.checkText(), room-labelCol, 2)
			out = append(out, bodyLine{row: i, kind: lineRow, text: clipMiddle(lines[0], room-labelCol), drop: -1})
			for _, l := range lines[1:] {
				out = append(out, bodyLine{row: i, kind: lineWrap, text: clipMiddle(l, room-labelCol), drop: -1})
			}
			ds, kept := r.details(), r.keptDetail()
			for k, d := range ds {
				drop := -1
				if k != kept {
					drop, entries = entries, entries+1
				}
				for _, l := range wrap(d.text, room-detailCol, 0) {
					out = append(out, bodyLine{row: i, kind: lineDetail, text: clipMiddle(l, room-detailCol), role: d.role, drop: drop})
				}
			}
			checkBlank = len(ds) > 0
		default:
			out = append(out, bodyLine{row: i, kind: lineRow, drop: -1})
		}
	}
	keepBlanks, keepEntries := blanks-c.blanks, entries-c.details
	kept := out[:0]
	for _, ln := range out {
		switch {
		case ln.kind == lineBlank && ln.drop >= keepBlanks:
		case ln.kind == lineDetail && ln.drop >= 0 && ln.drop >= keepEntries:
		default:
			kept = append(kept, ln)
		}
	}
	return kept
}

// detailEntries is how many detail entries the ladder may drop.
func detailEntries(lines []bodyLine) int {
	n := 0
	for _, ln := range lines {
		if ln.kind == lineDetail && ln.drop >= 0 {
			n = max(n, ln.drop+1)
		}
	}
	return n
}

// countLines is how many of lines are of kind k.
func countLines(lines []bodyLine, k lineKind) int {
	n := 0
	for _, ln := range lines {
		if ln.kind == k {
			n++
		}
	}
	return n
}

// pages names the page span a fold stands for: "pages 2-4".
func pages(from, to int) string { return fmt.Sprintf("pages %d-%d", from, to) }

// doneFold is the done rows as one row: the worst mark, "pages 1-n" and their names.
func (f *Form) doneFold() bodyLine {
	first, marks, names := -1, []Mark{}, []string{}
	for i := range f.Rows {
		if f.Rows[i].Kind == RowDone && f.shown(i) {
			if first < 0 {
				first = i
			}
			marks, names = append(marks, f.Rows[i].Mark), append(names, f.Rows[i].Label)
		}
	}
	return bodyLine{row: first, kind: lineFold, mark: Worst(marks...), label: pages(1, len(names)),
		text: strings.Join(names, " · "), drop: -1}
}

// aheadFold is the ahead page rows as one row: "pages n-m" and their names.
func (f *Form) aheadFold() bodyLine {
	first, names := -1, []string{}
	for i := range f.Rows {
		if f.Rows[i].Kind == RowAhead && f.shown(i) {
			if first < 0 {
				first = i
			}
			names = append(names, f.Rows[i].Label)
		}
	}
	last := max(f.Step.Of, len(names))
	return bodyLine{row: first, kind: lineFold, mark: MarkAhead, label: pages(last-len(names)+1, last),
		text: strings.Join(names, " · "), drop: -1}
}

// passedFold is section g's passed checks as one check: "n passed — names".
func (f *Form) passedFold(groups []int, g int) bodyLine {
	first, names := -1, []string{}
	for i := range f.Rows {
		if groups[i] == g && f.passed(i) {
			if first < 0 {
				first = i
			}
			names = append(names, f.Rows[i].Label)
		}
	}
	return bodyLine{row: first, kind: lineFold, mark: MarkDone, label: fmt.Sprintf("%d passed", len(names)),
		text: strings.Join(names, ", "), drop: -1}
}

// headRows splits the header into its fact rows and its prose rows on a w-column terminal.
func (f *Form) headRows(w int) (facts, prose [][]string) {
	var fs, ps []string
	for _, line := range f.Header {
		if _, ok := asFact(line); ok {
			fs = append(fs, line)
			continue
		}
		ps = append(ps, strings.TrimSpace(line))
	}
	width := edge(w) - factCol(w)
	return factRows(fs, width), factRows(ps, width)
}

// checklistLadder lays a checklist out on w by h: it drops or folds one thing at a time, in the
// ladder's order, and stops at the first fit; past the last rung the body scrolls.
// SPEC: _spec/internal/choiceui/wireframe.puml (THE CHECKLIST CARD)
func (f *Form) checklistLadder(w, h int) layout {
	help := f.maxHelpLines(helpWidth(w))
	facts, prose := f.headRows(w)
	lay := layout{helpSlot: help, header: len(facts) > 0}
	fixed := func(c cut) int {
		n := cardRows + len(facts) + help
		if f.skips() {
			n += skipRows
		}
		if !c.prose {
			n += len(prose)
		}
		if !c.head {
			n++
		}
		if !c.under && help > 0 {
			n++
		}
		return n
	}
	var c cut
	fits := func() bool {
		n := len(f.checklistLines(w, c))
		if fixed(c)+n > h {
			return false
		}
		lay.body, lay.cut = n, c
		return true
	}
	if fits() {
		return lay
	}
	full := f.checklistLines(w, cut{})
	for range countLines(full, lineBlank) {
		if c.blanks++; fits() {
			return lay
		}
	}
	rungs := []struct {
		want  bool
		apply func()
	}{
		{help > 0, func() { c.under = true }},
		{len(prose) > 0, func() { c.prose = true }},
		{f.foldsPassed(), func() { c.passed = true }},
	}
	for _, r := range rungs {
		if r.want {
			if r.apply(); fits() {
				return lay
			}
		}
	}
	for range detailEntries(f.checklistLines(w, c)) {
		if c.details++; fits() {
			return lay
		}
	}
	rungs = []struct {
		want  bool
		apply func()
	}{
		{f.countShown(RowDone) > 1, func() { c.done = true }},
		{true, func() { c.head = true }},
		{f.countShown(RowAhead) > 1, func() { c.ahead = true }},
	}
	for _, r := range rungs {
		if r.want {
			if r.apply(); fits() {
				return lay
			}
		}
	}
	lines := len(f.checklistLines(w, c))
	lay.cut = c
	lay.body = clampInt(h-fixed(c), 0, lines)
	lay.tooSmall = lines == 0 || lay.body < min(minBodyLines, lines)
	return lay
}

// cellName is how a cell names option opt: its Paint label, else the option.
func (r *Row) cellName(opt string) string {
	if s, ok := r.Paint[opt]; ok && s != "" {
		return s
	}
	return optionName(opt)
}

// checklistHelp is the help under a checklist for the cursor on row cursor: the running row's
// line, the blocker, a field's reason, or the answer's help; at most helpMost lines, the last cut
// with … when more were left.
func (f *Form) checklistHelp(cursor, width int) []helpLine {
	var out []helpLine
	switch {
	case f.Progress != nil:
		out = f.progressHelp(width)
	case f.Blocked != "":
		for _, l := range wrap(f.Blocked, width, 2) {
			out = append(out, helpLine{text: l, warn: true})
		}
	case !f.stop(cursor):
		return nil
	default:
		out = f.rowHelp(&f.Rows[cursor], width)
	}
	if len(out) > helpMost {
		out = out[:helpMost]
		last := &out[helpMost-1]
		last.text = clip(last.text+"…", width)
	}
	return out
}

// rowHelp is an answer row's help on a checklist: a field's "› label — reason", else the
// answer's help with the option named as its cell names it.
func (f *Form) rowHelp(r *Row, width int) []helpLine {
	if r.Field {
		if r.Reason == "" {
			return nil
		}
		var out []helpLine
		for _, l := range wrap(cursorGlyph(f.Glyphs)+" "+r.Label+" — "+r.Reason, width, 2) {
			out = append(out, helpLine{text: l})
		}
		return out
	}
	out := r.helpNamed(width, r.cellName)
	if plainGlyphs(f.Glyphs) {
		for i := range out {
			if rest, ok := strings.CutPrefix(out[i].text, "› "); ok {
				out[i].text = cursorGlyph(f.Glyphs) + " " + rest
			}
		}
	}
	return out
}

// progressHelp is the running row's help: "<label> — <what it does>".
func (f *Form) progressHelp(width int) []helpLine {
	pr := f.Progress
	if pr == nil || pr.Help == "" || pr.label() == "" {
		return nil
	}
	var out []helpLine
	for _, l := range wrap(pr.label()+" — "+pr.Help, width, 2) {
		out = append(out, helpLine{text: l})
	}
	return out
}

// checklistHelpSlot is the rows the help holds under a checklist: the most any answer, field,
// run or blocker needs, at most helpMost, so moving the cursor never moves a row.
func (f *Form) checklistHelpSlot(width int) int {
	most := 0
	for i := range f.Rows {
		r := f.Rows[i]
		if r.Kind != RowAnswer {
			continue
		}
		if r.Field {
			most = max(most, len(f.rowHelp(&r, width)))
			continue
		}
		for j := range r.Options {
			probe := r
			probe.More, probe.Lists = nil, nil
			probe.Selected, probe.Hover = j, j
			most = max(most, len(f.rowHelp(&probe, width)))
		}
	}
	most = max(most, len(f.progressHelp(width)))
	if f.Blocked != "" {
		most = max(most, len(wrap(f.Blocked, width, 2)))
	}
	return min(most, helpMost)
}

// skips reports whether a checklist draws its Skip button: at rest, when it has one.
func (f *Form) skips() bool { return f.Skip != "" && f.Progress == nil }

// enterWord is what Enter does on a checklist: "run n steps", "next", "finish".
func (f *Form) enterWord() string {
	if f.EnterWord != nil {
		if s := f.EnterWord(f); s != "" {
			return s
		}
	}
	return "next"
}

// anyStop reports whether some row can hold the cursor.
func (f *Form) anyStop() bool {
	for i := range f.Rows {
		if f.stop(i) {
			return true
		}
	}
	return false
}

// checklistKeys is a checklist's key line before narrowing: the run's progress while it runs;
// at rest the keys the cursor's row takes, the scroll keys while the body scrolls, what Enter
// does, and the way out.
// SPEC: _spec/internal/choiceui/wireframe.puml (THE CHECKLIST CARD)
func (f *Form) checklistKeys(scrolls bool, cursor int) []hintPart {
	if pr := f.Progress; pr != nil {
		if pr.Stopping {
			return []hintPart{{"", "stopping after " + pr.label() + " …", 9}}
		}
		return []hintPart{{"", fmt.Sprintf("running %d of %d — %s", pr.At+1, len(pr.Plan), pr.label()), 9}, {"ctrl+c", "stop", 2}}
	}
	esc := hintPart{"esc", "cancel", 5}
	switch {
	case f.Step.backable():
		esc.word, esc.keep = "back", 6
	case f.Step.wizard():
		esc.word, esc.keep = "quit", 6
	}
	if f.Esc != "" {
		esc.word = f.Esc
	}
	if list := f.openList(cursor); list != nil && f.Blocked == "" {
		return f.checklistTail(listParts(list, esc))
	}
	var parts []hintPart
	if f.Blocked == "" && f.anyStop() {
		parts = append(parts, hintPart{"↑↓", "row", 3})
	}
	if f.Blocked == "" && f.stop(cursor) {
		r := &f.Rows[cursor]
		switch {
		case r.Field:
			parts = append(parts, hintPart{"type", "to edit", 3})
		case r.Multi:
			parts = append(parts, hintPart{"←→", "move", 3}, hintPart{"space", "toggle", 2})
		case len(r.Options) > 0:
			parts = append(parts, hintPart{"←→", "choose", 3})
		}
		if list := f.listAt(cursor); list != nil && list.Filter {
			parts = append(parts, hintPart{"/", "filter", 2})
		}
	}
	if scrolls {
		parts = append(parts, hintPart{"pgup/pgdn", "scroll", 4})
	}
	if f.Blocked == "" {
		enter := hintPart{"enter", f.enterWord(), 9}
		if f.onSkip {
			parts, enter.word = parts[:0], "skip"
			if f.anyStop() {
				parts = append(parts, hintPart{"↑↓", "row", 3})
			}
		}
		parts = append(parts, enter)
	}
	return f.checklistTail(append(parts, esc))
}

// plainArrows are the arrow keys as the ASCII and Off tiers name them.
var plainArrows = map[string]string{"↑↓": "up/down", "←→": "left/right"}

// checklistTail ends a checklist's key line with the quit key on a wizard's page, its arrows named
// in words under the ASCII and Off tiers.
func (f *Form) checklistTail(parts []hintPart) []hintPart {
	if f.Step.wizard() {
		parts = append(parts, hintPart{"ctrl+c", "quit", 2})
	}
	if plainGlyphs(f.Glyphs) {
		for i, p := range parts {
			if word, ok := plainArrows[p.key]; ok {
				parts[i].key = word
			}
		}
	}
	return parts
}

// fitOutcome is an outcome cut to room columns: whole when it fits, else its brief count form,
// else that form with its first item cut with … so the rest stays whole.
func fitOutcome(outcome, brief string, room int) string {
	if textWidth(outcome) <= room {
		return outcome
	}
	s := outcome
	if brief != "" {
		s = brief
	}
	if textWidth(s) <= room {
		return s
	}
	if first, rest, ok := strings.Cut(s, " · "); ok {
		tail := " · " + rest
		if keep := room - textWidth(tail); keep >= 2 {
			return clip(first, keep) + tail
		}
	}
	return clip(s, room)
}

// clipMiddle is text cut to width columns, its head and tail kept around "…".
func clipMiddle(text string, width int) string {
	r := []rune(text)
	if len(r) <= width || width < 3 {
		return clip(text, width)
	}
	tail := (width - 1) / 2
	return string(r[:width-1-tail]) + "…" + string(r[len(r)-tail:])
}

// drawChecklist paints the form as a checklist card: the head with its facts, prose and blank;
// the body window; the foot and key line; then a blank and the help under the card.
// SPEC: _spec/internal/choiceui/wireframe.puml (THE CHECKLIST CARD)
func (f *Form) drawChecklist(s *screen, cursor, tick int) {
	p := styles()
	w, h := s.Size()
	lay := f.layout(w, h)
	if lay.tooSmall {
		keys := slices.DeleteFunc(f.checklistKeys(false, -1), func(p hintPart) bool { return p.word == "row" })
		drawTooSmall(s, w, h, f.minHeight(w), joinHint(keys))
		return
	}
	k := newCard(w, f.Glyphs, p)
	k.label = f.Border
	c := newCanvas(s)
	k.head(c, f.Title, f.headChip(w, cursor))
	f.at = cardAt{head: c.y - 1, w: k.w, framed: k.framed}
	room := listRoom{row: -1, head: c.y}
	facts, prose := f.headRows(w)
	for _, row := range facts {
		x := factCol(w)
		for _, fact := range row {
			putHeader(c.put, p, x, fact)
			x += textWidth(fact) + len(factJoin)
		}
		k.walls(c, false)
		c.y++
	}
	if !lay.cut.prose {
		for _, row := range prose {
			k.line(c, false, func() { c.put(factCol(w), p.body, strings.Join(row, factJoin)) })
		}
	}
	if !lay.cut.head {
		k.walls(c, false)
		c.y++
	}

	lines := f.checklistLines(w, lay.cut)
	f.scroll = scrollTo(f.scroll, lay.body, lines, cursor)
	g := newGutter(f.scroll, lay.body, len(lines))
	groups := f.groups()
	mark := -1
	if f.Progress == nil && f.Blocked == "" && !f.onSkip && f.stop(cursor) {
		mark = cursor
	}
	lit := func(row int) bool { return mark >= 0 && row >= 0 && groups[row] == groups[mark] }
	named := headingsInWindow(lines, f.scroll, lay.body)
	pt := pitch(k.w, f.lastCol())
	bodyTop := c.y
	window := min(lay.body, len(lines)-f.scroll)
	c.clipTo(bodyTop, bodyTop+lay.body)
	c.right = k.edge
	for i := range window {
		c.y = bodyTop + i
		ln := lines[f.scroll+i]
		if ln.kind == lineRow && ln.row == mark && f.openList(mark) != nil {
			room.row = c.y
		}
		f.drawChecklistLine(c, p, k, ln, mark, pt, tick, lit(ln.row), named[ln.row])
	}
	c.unclip()
	c.clipTo(bodyTop, bodyTop+lay.body)
	for i := range window {
		c.y = bodyTop + i
		ln := lines[f.scroll+i]
		if ln.kind != lineHeading {
			k.wallL(c, ln.kind != lineBlank && lit(ln.row))
		}
		k.wallR(c)
		if glyph, thumb := g.glyph(i, plainGlyphs(f.Glyphs)); glyph != "" {
			st := p.frame
			if thumb {
				st = p.rail
			}
			c.put(k.gutterX(), st, glyph)
		}
	}
	c.unclip()

	c.y = bodyTop + lay.body
	if f.skips() {
		k.walls(c, false)
		c.y++
		f.drawSkip(c, p)
		k.walls(c, false)
		c.y++
	}
	room.foot = c.y
	k.foot(c)
	c.y++
	scrolls := 0
	if g.scrolls() {
		scrolls = 1
	}
	f.drawHint(c, p, w, scrolls, mark)
	k.walls(c, false)
	c.y++
	k.bottom(c)
	f.at.bottom = c.y
	c.y++
	room.under = c.y
	if lay.helpSlot > 0 {
		if !lay.cut.under {
			c.y++
		}
		top := c.y
		for i, line := range f.checklistHelp(mark, helpWidth(w)) {
			c.y = top + i
			putHelp(c, p, line)
		}
	}
	if room.row >= 0 {
		r := &f.Rows[mark]
		col := r.activeMore().Cell
		if col < len(r.Cols) {
			col = r.Cols[col]
		}
		f.drawMore(c, p, k, mark, optOrigin+col*pt, room)
	}
}

// drawChecklistLine paints one checklist body line: an answer row or a stub as any form draws it;
// a status, done or ahead row as mark, label and outcome; a check as mark and "name — message";
// a check's next line, detail line or a fold.
func (f *Form) drawChecklistLine(c *canvas, p palette, k card, ln bodyLine, cursor, pt, tick int, lit, named bool) {
	switch ln.kind {
	case lineBlank:
		return
	case lineWrap:
		c.put(labelCol, p.body, ln.text)
		return
	case lineDetail:
		st := p.idle
		if ln.role == detailNote {
			st = p.aside
		}
		c.put(detailCol, st, ln.text)
		return
	case lineFold:
		c.put(markCol, ln.mark.style(p), ln.mark.Glyph(f.Glyphs, tick))
		if f.Rows[ln.row].Kind == RowCheck {
			c.put(labelCol, p.body, clip(ln.label+" — "+ln.text, k.edge-labelCol))
			return
		}
		f.drawStatus(c, p, k, ln.label, ln.text, "", 0, f.Rows[ln.row].Kind)
		return
	}
	r := &f.Rows[ln.row]
	if ln.kind == lineHeading || r.Kind == RowAnswer {
		f.drawBodyLine(c, p, k, ln, cursor, pt, lit, named)
		return
	}
	c.put(markCol, r.Mark.style(p), r.Mark.Glyph(f.Glyphs, tick))
	if r.Kind == RowCheck {
		c.put(labelCol, p.body, ln.text)
		return
	}
	f.drawStatus(c, p, k, r.Label, r.Outcome, r.Brief, r.Indent, r.Kind)
}

// drawStatus paints a status row's label at column 4 plus indent and its outcome at column 18: a
// done row's outcome as fitOutcome cuts it, any other's cut at its end.
func (f *Form) drawStatus(c *canvas, p palette, k card, label, outcome, brief string, indent int, kind RowKind) {
	labelSt, outSt := p.body, p.body
	switch kind {
	case RowDone:
		outSt = p.idle
	case RowAhead:
		labelSt, outSt = p.idle, p.idle
	}
	x := labelCol + max(0, indent)
	c.put(x, labelSt, clipRunes(label, max(1, outcomeCol-1-x)))
	room := k.edge - outcomeCol
	text := clip(outcome, room)
	if kind == RowDone {
		text = fitOutcome(outcome, brief, room)
	}
	c.put(outcomeCol, outSt, text)
}
