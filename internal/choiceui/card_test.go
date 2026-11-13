// SPEC: _spec/internal/choiceui/wireframe.puml
package choiceui

import (
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

// One predicate decides the chip: the head draws it exactly when the key line drops the page,
// and the painted key line is the plain projection tests and e2e read.
func TestTheChipAndTheKeyLineAgreeOnThePage(t *testing.T) {
	t.Parallel()
	drew, kept := false, false
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
			if want := f.headChip(w) != ""; chip != want {
				t.Errorf("title %d, width %d: head draws the chip %v, headChip says %v: %q", len(title), w, chip, want, head)
			}
			if chip != (framed(w) && chipFits(textWidth(title), textWidth(" page 2/3 "), cardWidth(w))) {
				t.Errorf("title %d, width %d: the chip ignores chipFits: %q", len(title), w, head)
			}
			hint := f.hint(w, 0)
			if chip && strings.Contains(hint, "page") {
				t.Errorf("title %d, width %d: chip and key line both name the page: %q", len(title), w, hint)
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
			kept = kept || (framed(w) && !chip && strings.Contains(hint, "page 2/3"))
		}
	}
	if !drew || !kept {
		t.Errorf("the sweep must reach both cases: chip drawn %v, page kept on a framed key line %v", drew, kept)
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
