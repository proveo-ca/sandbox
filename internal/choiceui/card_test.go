// SPEC: _spec/internal/choiceui/wireframe.puml
package choiceui

import (
	"fmt"
	"strings"
	"testing"
)

// Every row from the top border to the bottom border ends on a wall at column 0 and
// at the card's last column; under 40 columns no frame draws and the gutter keeps column 0.
func TestEveryCardRowHasAWallAtBothEdges(t *testing.T) {
	t.Parallel()
	g := glyphsFor(GlyphsNerd)
	left := g.wallL + g.frameTL + g.frameBL + g.joinL
	right := g.wallR + g.frameTR + g.frameBR + g.joinR + "▲▼░"
	marks := "▲▼░"
	for w := 12; w <= 160; w++ {
		scrolling := 0
		for h := 60; h >= 8 && scrolling == 0; h-- {
			if lay := tallForm().layout(w, h); !lay.tooSmall && lay.body < tallForm().rowsHeight() {
				scrolling = h
			}
		}
		if scrolling == 0 {
			t.Fatalf("width %d: the fixture never scrolls, so the gutter's home goes unchecked", w)
		}
		for _, h := range []int{scrolling, 60} {
			f := tallForm()
			lay := f.layout(w, h)
			if lay.tooSmall {
				t.Fatalf("%dx%d: the fixture is too small to draw, so the sweep proves nothing", w, h)
			}
			lines := renderAt(t, f, 0, w, h)
			scrolls := lay.body < f.rowsHeight()
			top, bottom := -1, -1
			for y, l := range lines {
				switch {
				case top < 0 && strings.HasPrefix(l, g.frameTL):
					top = y
				case top >= 0 && bottom < 0 && strings.HasPrefix(l, g.frameBL):
					bottom = y
				}
			}
			if !framed(w) {
				if top >= 0 {
					t.Errorf("%dx%d: under %d columns no frame draws:\n%s", w, h, cardMin, strings.Join(lines, "\n"))
				}
				if scrolls && !anyLine(lines, func(r []rune) bool { return len(r) > 0 && strings.ContainsRune(marks, r[0]) }) {
					t.Errorf("%dx%d: unframed, the scroll marks ride column 0:\n%s", w, h, strings.Join(lines, "\n"))
				}
				continue
			}
			cw := cardWidth(w)
			if top < 0 || bottom < 0 {
				t.Fatalf("%dx%d: no whole card (top %d, bottom %d):\n%s", w, h, top, bottom, strings.Join(lines, "\n"))
			}
			for y := top; y <= bottom; y++ {
				r := []rune(lines[y])
				if len(r) != cw || !strings.ContainsRune(left, r[0]) || !strings.ContainsRune(right, r[cw-1]) {
					t.Errorf("%dx%d row %d: not walled at 0 and %d: %q", w, h, y, cw-1, lines[y])
				}
			}
			if scrolls && !anyLine(lines[top:bottom], func(r []rune) bool { return len(r) == cw && strings.ContainsRune(marks, r[cw-1]) }) {
				t.Errorf("%dx%d: the scroll marks ride the right wall:\n%s", w, h, strings.Join(lines, "\n"))
			}
		}
	}
}

func anyLine(lines []string, ok func([]rune) bool) bool {
	for _, l := range lines {
		if ok([]rune(l)) {
			return true
		}
	}
	return false
}

// The drawn frame keeps "enter accept" at every width the plain key line kept it.
func TestEnterAcceptSurvivesEveryWidthOnTheDrawnFrame(t *testing.T) {
	t.Parallel()
	for _, mk := range []func() *Form{
		func() *Form { return stepForm(2, 3) },
		func() *Form { return stepForm(0, 0) },
		tallForm,
		budgetForm,
	} {
		for w := len("enter accept"); w <= 160; w++ {
			f := mk()
			out := strings.Join(renderAt(t, f, 0, w, 40), "\n")
			if strings.Contains(out, "terminal too small") || !strings.Contains(out, "enter accept") {
				t.Fatalf("%q at width %d lost \"enter accept\":\n%s", f.Title, w, out)
			}
		}
	}
}

// One predicate decides the chip: the head draws it exactly when the key line drops the page, on
// the title row under 40 columns too; the page shows wherever the key line has room for it beside
// enter and esc; and the painted key line is the plain projection tests and e2e read.
func TestTheChipAndTheKeyLineAgreeOnThePage(t *testing.T) {
	t.Parallel()
	drew, drewUnframed, kept := false, false, false
	for _, title := range []string{"step", strings.Repeat("t", 60), strings.Repeat("t", 100), strings.Repeat("t", 104)} {
		for w := 12; w <= 160; w++ {
			f := stepForm(2, 3)
			f.Title = title
			lines := renderAt(t, f, 0, w, 30)
			head := lines[0]
			if framed(w) {
				head = lines[1]
			}
			chip := strings.Contains(head, "page 2/3")
			if want := f.headChip(w, 0) != ""; chip != want {
				t.Errorf("title %d, width %d: head draws the chip %v, headChip says %v: %q", len(title), w, chip, want, head)
			}
			keyHolds := keyRoom(w) >= textWidth("page 2/3 · enter accept · esc back")
			cut := !keyHolds && chipFits(titleKeep+1, textWidth(" page 2/3 "), w)
			if want := chipFits(textWidth(title), textWidth(" page 2/3 "), w) || cut; chip != want {
				t.Errorf("title %d, width %d: chip drawn %v, want %v (whole title fits, or the key line drops the page and a cut title fits): %q", len(title), w, chip, want, head)
			}
			if chip && !framed(w) && (!strings.HasSuffix(head, " page 2/3") || textWidth(head) != w-1) {
				t.Errorf("title %d, width %d: unframed, the chip ends the title row: %q", len(title), w, head)
			}
			hint := f.hint(w, 0, 0)
			if chip && strings.Contains(hint, "page") {
				t.Errorf("title %d, width %d: chip and key line both name the page: %q", len(title), w, hint)
			}
			if keyRoom(w) >= textWidth("page 2/3 · enter accept · esc back") && !chip && !strings.Contains(hint, "page 2/3") {
				t.Errorf("title %d, width %d: neither the head nor the key line names the page: %q", len(title), w, hint)
			}
			key := -1
			for y, l := range lines {
				if strings.Contains(l, "enter accept") {
					key = y
				}
			}
			if key < 0 {
				t.Fatalf("title %d, width %d: no key line", len(title), w)
			}
			r := []rune(lines[key])
			if framed(w) {
				r = r[inset(w) : len(r)-1]
			}
			if got := strings.TrimRight(string(r), " "); got != strings.TrimRight(hint, " ") {
				t.Errorf("title %d, width %d: painted key line %q, plain %q", len(title), w, got, hint)
			}
			drew = drew || chip
			drewUnframed = drewUnframed || (chip && !framed(w))
			kept = kept || (framed(w) && !chip && strings.Contains(hint, "page 2/3"))
		}
	}
	if !drew || !kept || !drewUnframed {
		t.Errorf("the sweep must reach every case: chip drawn %v, unframed %v, page kept on a framed key line %v", drew, drewUnframed, kept)
	}
}

// A row whose stub scrolled off names itself; with the stub on screen it does not.
func TestARowWhoseStubScrolledOffNamesItself(t *testing.T) {
	t.Parallel()
	last := len(tallForm().Rows) - 1
	reached := false
	for h := 8; h <= 40; h++ {
		lines := renderAt(t, tallForm(), last, 80, h)
		joined := strings.Join(lines, "\n")
		if strings.Contains(joined, "terminal too small") || strings.Contains(joined, "├── execution") ||
			!strings.Contains(joined, "[ ] host") {
			continue
		}
		reached = true
		for _, l := range lines {
			if strings.Contains(l, "[ ] host") && !strings.Contains(l, "execution") {
				t.Errorf("h=%d: with its stub off screen the row must name itself: %q", h, l)
			}
		}
	}
	if !reached {
		t.Fatal("never reached a height with the stub off screen and the row on it; the test proves nothing")
	}
	for _, l := range renderAt(t, tallForm(), last, 80, 60) {
		if strings.Contains(l, "[ ] host") && strings.Contains(l, "execution") {
			t.Errorf("the stub is on screen, yet the row repeats its name: %q", l)
		}
	}
}

// The cursor's section lights its stub and the left wall beside its rows; every other frame cell is grey.
func TestTheCursorsSectionLightsItsStubAndWall(t *testing.T) {
	t.Parallel()
	p := styles()
	for _, cursor := range []int{0, 3} {
		f := budgetForm()
		s := simScreen(t, 120, 40)
		f.draw(s, cursor, 0)
		lines := screenLines(s)
		lit := "reach"
		if cursor == 3 {
			lit = "execution"
		}
		section, stubs, walls := "", 0, 0
		for y, l := range lines {
			r := []rune(l)
			if len(r) == 0 {
				continue
			}
			_, _, wallSt, _ := s.GetContent(0, y)
			switch {
			case strings.HasPrefix(l, "├── "):
				section = strings.Fields(l)[1]
				stubs++
				_, _, nameSt, _ := s.GetContent(4, y)
				want, wantName := p.frame, p.frame
				if section == lit {
					want, wantName = p.rail, p.title
				}
				if !sameStyle(wallSt, want) || !sameStyle(nameSt, wantName) {
					t.Errorf("cursor %d: the %s stub draws %q, its name %q", cursor, section, wallSt.Render("x"), nameSt.Render("x"))
				}
			case r[0] == '│' && section != "":
				walls++
				want := p.frame
				if section == lit {
					want = p.rail
				}
				if !sameStyle(wallSt, want) {
					t.Errorf("cursor %d: the wall beside %q draws %q", cursor, l, wallSt.Render("x"))
				}
				if _, _, right, _ := s.GetContent(cardWidth(120)-1, y); !sameStyle(right, p.frame) {
					t.Errorf("cursor %d: the right wall beside %q draws %q", cursor, l, right.Render("x"))
				}
			case strings.HasPrefix(l, "├─") || strings.HasPrefix(l, "╰"):
				section = ""
			}
		}
		if stubs != 2 || walls != 4 {
			t.Errorf("cursor %d: checked %d stubs and %d walled rows, want 2 and 4", cursor, stubs, walls)
		}
	}
	if sameStyle(p.rail, p.frame) || p.rail.GetBold() {
		t.Errorf("the rail is brand and not bold, apart from the grey frame: rail %q frame %q", p.rail.Render("x"), p.frame.Render("x"))
	}
}

// A stub grows for its name, and a name too long for the card clips before the wall.
func TestAStubClipsInsideTheCard(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("n", 90)
	f := &Form{Rows: []Row{{Label: "a", Heading: long, Options: []string{"x"}, Multi: true, Divider: true}}}
	for _, w := range []int{40, 80, 120, 160} {
		cw := cardWidth(w)
		for _, l := range renderAt(t, f, 0, w, 20) {
			r := []rune(l)
			if !strings.HasPrefix(l, "├──") || strings.HasSuffix(l, "┤") {
				continue
			}
			if len(r) != cw || r[cw-1] != '│' || r[cw-2] != ' ' {
				t.Errorf("width %d: the stub runs into the wall: %q", w, l)
			}
			fits := len(long)+7 <= cw-2
			if fits != strings.Contains(l, long) || fits == strings.Contains(l, "…") {
				t.Errorf("width %d: the name must show whole when it fits and clip with … when not: %q", w, l)
			}
		}
	}
}

// pageCase is a scope or checks page drawn at w by h, with the chip its head should carry and the
// plain key line it should paint.
type pageCase struct {
	name string
	draw func(w, h int) (lines []string, chip, hint string)
}

func pageCases() []pageCase {
	scope := func(mk func() *ScopePage) func(w, h int) ([]string, string, string) {
		return func(w, h int) ([]string, string, string) {
			sp := mk()
			lines := scopeLines(sp, w, h)
			return lines, sp.chip(w), sp.hint(w, len(sp.Tree.Rows())-sp.top-sp.layout(h).window)
		}
	}
	check := func(mk func() *CheckPage) func(w, h int) ([]string, string, string) {
		return func(w, h int) ([]string, string, string) {
			cp := mk()
			lines := checkLines(cp, w, h)
			return lines, cp.chip(w), cp.hint(w, len(cp.lines())-cp.top-cp.layout(h).window)
		}
	}
	return []pageCase{
		{"scope", scope(scopePage)},
		{"scope-long", scope(func() *ScopePage {
			sp := scopePage()
			sp.Root = strings.Repeat("r", 70)
			sp.Tree.Place("services")
			sp.Tree.Fold()
			return sp
		})},
		{"scope-filter", scope(func() *ScopePage { sp := scopePage(); sp.filtering = true; sp.Tree.SetFilter("bill"); return sp })},
		{"scope-ascii", scope(func() *ScopePage { sp := scopePage(); sp.Glyphs = GlyphsASCII; return sp })},
		{"checks", check(checkPage)},
		{"checks-long", check(func() *CheckPage {
			cp := checkPage()
			cp.Rows[2].Detail = strings.Repeat("d", 150)
			cp.Rows[2].Hint = strings.Repeat("h", 150)
			return cp
		})},
		{"checks-ascii", check(func() *CheckPage { cp := checkPage(); cp.Glyphs = GlyphsASCII; return cp })},
	}
}

// The scope and checks pages are cards: every row from the top border to the bottom border is walled
// at column 0 and the card's last column, text stops short of the column inside the right wall, the
// head carries the step chip exactly when the key line drops the page, and the painted key line is
// the plain one. Under 40 columns no frame draws.
func TestTheScopeAndCheckPagesAreCards(t *testing.T) {
	t.Parallel()
	for _, pc := range pageCases() {
		g := glyphsFor(GlyphsNerd)
		if strings.HasSuffix(pc.name, "ascii") {
			g = glyphsFor(GlyphsASCII)
		}
		drew, kept := false, false
		for w := 12; w <= 160; w++ {
			for _, h := range []int{8, 12, 24, 45} {
				lines, chip, hint := pc.draw(w, h)
				out := strings.Join(lines, "\n")
				if strings.Contains(out, "terminal too small") {
					continue
				}
				top, bottom := -1, -1
				for y, l := range lines {
					switch {
					case top < 0 && strings.HasPrefix(l, g.frameTL):
						top = y
					case top >= 0 && strings.HasPrefix(l, g.frameBL):
						bottom = y
					}
				}
				if !framed(w) {
					if top >= 0 {
						t.Errorf("%s %dx%d: under %d columns no frame draws:\n%s", pc.name, w, h, cardMin, out)
					}
					if chip != "" && !strings.Contains(out, strings.TrimRight(chip, " ")) {
						t.Errorf("%s %dx%d: the chip %q is missing from the title row:\n%s", pc.name, w, h, chip, out)
					}
					if chip != "" && strings.Contains(hint, "page") {
						t.Errorf("%s %dx%d: chip and key line both name the page: %q", pc.name, w, h, hint)
					}
					continue
				}
				cw := cardWidth(w)
				if top < 0 || bottom < top+4 {
					t.Fatalf("%s %dx%d: no whole card (top %d, bottom %d):\n%s", pc.name, w, h, top, bottom, out)
				}
				for y := top + 1; y < bottom; y++ {
					r := []rune(lines[y])
					rule := strings.HasPrefix(lines[y], g.joinL)
					if len(r) != cw || (!rule && (string(r[0]) != g.wallL || string(r[cw-1]) != g.wallR || r[cw-2] != ' ')) {
						t.Errorf("%s %dx%d row %d: not walled at 0 and %d with column %d blank: %q", pc.name, w, h, y, cw-1, cw-2, lines[y])
					}
				}
				if head := lines[top+1]; strings.Contains(head, "1/") != (chip != "") || !strings.Contains(head, chip) {
					t.Errorf("%s %dx%d: the head %q and the chip %q disagree", pc.name, w, h, head, chip)
				}
				if chip != "" && strings.Contains(hint, "page") {
					t.Errorf("%s %dx%d: chip and key line both name the page: %q", pc.name, w, h, hint)
				}
				if got := inner(lines[bottom-1], w)[1:]; got != strings.TrimRight(hint, " ") {
					t.Errorf("%s %dx%d: painted key line %q, plain %q", pc.name, w, h, got, hint)
				}
				drew = drew || chip != ""
				kept = kept || chip == "" && strings.Contains(hint, "page")
			}
		}
		if !drew || !kept {
			t.Errorf("%s: the sweep must reach both cases: chip drawn %v, page kept on a framed key line %v", pc.name, drew, kept)
		}
	}
}

// The step count never vanishes: under 40 columns the chip ends the title row when title + 2 + chip
// fit in w; where the key line cannot keep the page count, the head draws the chip beside the title
// cut with … to at least titleKeep runes; the frame shows the count once at most; and a key line
// with no chip above it drops "N more below" before the page count.
func TestTheStepCountNeverVanishes(t *testing.T) {
	t.Parallel()
	type stepPage struct {
		name, chip, count string
		draw              func(title string, w int) (lines []string, chip string)
		hint              func(title string, w, hidden int) string
	}
	form := func(title string) *Form { f := stepForm(2, 3); f.Title = title; return f }
	scope := func(title string) *ScopePage { sp := scopePage(); sp.Banner, sp.Title = nil, title; return sp }
	check := func(title string) *CheckPage { cp := checkPage(); cp.Banner, cp.Title = nil, title; return cp }
	pages := []stepPage{
		{"form", " page 2/3 ", "page 2/3",
			func(title string, w int) ([]string, string) {
				f := form(title)
				return renderAt(t, f, 0, w, 30), f.headChip(w, 0)
			},
			func(title string, w, hidden int) string { return form(title).hint(w, hidden, 0) }},
		{"scope", " scope · 1/2 ", "page 1/2",
			func(title string, w int) ([]string, string) {
				sp := scope(title)
				return scopeLines(sp, w, 24), sp.chip(w)
			},
			func(title string, w, hidden int) string { return scope(title).hint(w, hidden) }},
		{"checks", " checks · 1/4 ", "page 1/4",
			func(title string, w int) ([]string, string) {
				cp := check(title)
				return checkLines(cp, w, 24), cp.chip(w)
			},
			func(title string, w, hidden int) string { return check(title).hint(w, hidden) }},
	}
	for _, pg := range pages {
		unframed, ranked, cut := false, false, false
		label := strings.TrimSpace(pg.chip)
		for _, title := range []string{"t", strings.Repeat("t", 22), strings.Repeat("t", 100)} {
			for w := 12; w <= 160; w++ {
				lines, chip := pg.draw(title, w)
				out := strings.Join(lines, "\n")
				shown := strings.Count(out, label)
				if label != pg.count {
					shown += strings.Count(out, pg.count)
				}
				if shown > 1 {
					t.Errorf("%s title %d width %d: the step count shows %d times:\n%s", pg.name, len(title), w, shown, out)
				}
				if shown == 0 && chipFits(titleKeep+1, textWidth(pg.chip), w) {
					t.Errorf("%s title %d width %d: the step count vanished where a cut title and the chip fit:\n%s", pg.name, len(title), w, out)
				}
				if chip != "" && !chipFits(textWidth(title), textWidth(chip), w) {
					cut = true
					head := ""
					for _, l := range lines {
						if strings.Contains(l, label) {
							head = l
						}
					}
					if dots := strings.Index(head, "…"); dots < 0 || dots > strings.Index(head, label) || !strings.Contains(head, strings.Repeat("t", titleKeep)) {
						t.Errorf("%s title %d width %d: the head must cut the title with … to at least %d runes before the chip: %q", pg.name, len(title), w, titleKeep, head)
					}
				}
				if !framed(w) && title == "t" {
					if room := textWidth(title)+2+textWidth(label) < w; room != (chip == pg.chip) {
						t.Errorf("%s width %d: title + 2 + chip fit in w %v, chip drawn %q", pg.name, w, room, chip)
					}
					if chip != "" {
						unframed = true
						if row := lines[0]; !strings.HasPrefix(row, "t ") || !strings.HasSuffix(row, strings.TrimRight(chip, " ")) || textWidth(row) != w-1 {
							t.Errorf("%s width %d: the chip %q must end the title row: %q", pg.name, w, chip, row)
						}
					}
				}
				if chip != "" {
					continue
				}
				hint := pg.hint(title, w, 7)
				if strings.Contains(hint, "7 more below") && !strings.Contains(hint, pg.count) {
					t.Errorf("%s width %d: %q kept \"7 more below\" over %q", pg.name, w, hint, pg.count)
				}
				ranked = ranked || (strings.Contains(hint, pg.count) && !strings.Contains(hint, "more below"))
			}
		}
		if !unframed || !ranked || !cut {
			t.Errorf("%s: the sweep must reach every case: unframed chip %v, page kept over more below %v, chip beside a cut title %v", pg.name, unframed, ranked, cut)
		}
	}
}

// Unframed, a stub starts at column 1 and a scroll mark at column 0 never covers its first rune.
func TestUnframedTheGutterNeverCoversAStub(t *testing.T) {
	t.Parallel()
	marks := "▲▼░"
	covered := 0
	for w := 20; w < cardMin; w++ {
		for h := 8; h <= 30; h++ {
			for cursor := range tallForm().Rows {
				f := tallForm()
				if f.layout(w, h).tooSmall {
					continue
				}
				for _, l := range renderAt(t, f, cursor, w, h) {
					r := []rune(l)
					for _, name := range []string{"reach", "execution", "interface"} {
						if !strings.Contains(l, " "+name+" ─") {
							continue
						}
						if len(r) < 4 || (r[0] != ' ' && !strings.ContainsRune(marks, r[0])) || string(r[1:4]) != "── " || !strings.HasPrefix(string(r[4:]), name) {
							t.Errorf("%dx%d cursor %d: the stub must run from column 1 with its name at 4: %q", w, h, cursor, l)
						}
						if strings.ContainsRune(marks, r[0]) {
							covered++
						}
					}
				}
			}
		}
	}
	if covered == 0 {
		t.Error("no scroll mark ever shared a row with a stub; the test proves nothing")
	}
}

// Unframed, the facts start at column 2 and the on-enter list at 0, its numbers at 2 and their text at 5;
// framed, each moves two columns in.
func TestUnframedFormsKeepTheirColumns(t *testing.T) {
	t.Parallel()
	for _, w := range []int{36, 80} {
		f := stepForm(2, 3)
		f.Header = []string{"git: r", "keys: K"}
		f.OnEnter = func(*Form) []string { return []string{"action a"} }
		x := inset(w)
		lines := renderAt(t, f, 0, w, 30)
		col := func(s string) int {
			for _, l := range lines {
				if i := strings.Index(l, s); i >= 0 {
					return textWidth(l[:i])
				}
			}
			return -1
		}
		for s, want := range map[string]int{"git: r": x + 2, "on enter": x, "1  action a": x + 2, "action a": x + 5} {
			if got := col(s); got != want {
				t.Errorf("width %d: %q at column %d, want %d:\n%s", w, s, got, want, strings.Join(lines, "\n"))
			}
		}
	}
}

// An indented header line loses its indent and starts at the facts' column, whether it continues a
// fact or not: column 4 framed, column 2 unframed.
func TestIndentedHeaderLinesStartAtTheFactsColumn(t *testing.T) {
	t.Parallel()
	header := []string{"git:      repo on main", "keys:     K", "          A=1", "          B=2",
		"Host SSH agent   2 key(s)", "  SHA256:abc ed25519"}
	got := factRows(header, 100)
	want := [][]string{{"git: repo on main", "keys: K"}, {"A=1"}, {"B=2"}, {"Host SSH agent   2 key(s)"}, {"SHA256:abc ed25519"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("factRows = %q, want %q", got, want)
	}
	for _, w := range []int{36, 120} {
		f := &Form{Title: "t", Header: header, Rows: []Row{{Label: "a", Options: []string{"x"}}}}
		lines := renderAt(t, f, 0, w, 20)
		if want := map[bool]int{true: 4, false: 2}[framed(w)]; factCol(w) != want {
			t.Errorf("width %d: the facts' column is %d, want %d", w, factCol(w), want)
		}
		for s, want := range map[string]int{"A=1": factCol(w), "B=2": factCol(w), "SHA256:abc": factCol(w), "Host SSH": factCol(w)} {
			at := -1
			for _, l := range lines {
				if i := strings.Index(l, s); i >= 0 {
					at = textWidth(l[:i])
				}
			}
			if at != want {
				t.Errorf("width %d: %q at column %d, want %d:\n%s", w, s, at, want, strings.Join(lines, "\n"))
			}
		}
	}
}
