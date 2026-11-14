// SPEC: _spec/internal/choiceui/wireframe.puml
package choiceui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// bareCredentials is the run form's credentials row with sizes[i] entries behind cell i, drawn
// without tick boxes when bare, above a model row.
func bareCredentials(bare bool, sizes ...int) *Form {
	r := Row{Label: "credentials", Multi: true, Bare: bare, Locked: true, Options: []string{"API keys", "OAuth", "SSH", ".env"},
		Cols: []int{0, 1, 2, 3}, Lists: map[int]*More{}, Reason: "host credential inventory",
		Help: map[string]string{"API keys": "4 configured item(s); read-only"}}
	for i, n := range sizes {
		r.On = append(r.On, n > 0)
		if n == 0 {
			continue
		}
		names := make([]string, n)
		for j := range names {
			names[j] = fmt.Sprintf("entry-%03d", j)
		}
		r.Lists[i] = &More{Cell: i, Options: names, ReadOnly: true}
	}
	return &Form{Title: "run claudecode", Rows: []Row{counted(r),
		{Label: "model", Options: []string{"Qwen 3.8", "API keys"}, Selected: 1, Cols: []int{0, 1}}}}
}

// cellAt is the text of option j's cell on row y of a w-column frame, cut to its grid room.
func cellAt(lines []string, f *Form, y, j, w int) string {
	rs := []rune(lines[y])
	at := optOrigin + f.Rows[0].Cols[j]*pitch(cardWidth(w), f.lastCol())
	room := min(pitch(cardWidth(w), f.lastCol())-2, edge(w)-at)
	return strings.TrimRight(string(rs[at:min(at+room, len(rs))]), " ")
}

// A Bare checkbox row draws each cell from its own grid column with no tick box, so the counted
// credentials cells fit whole at 80 columns; the same row unflagged keeps "[x] " and "[ ] ".
func TestABareRowDrawsItsCellsWithoutTickBoxes(t *testing.T) {
	t.Parallel()
	for _, sizes := range [][]int{{4, 2, 1, 20}, {4, 0, 1, 20}} {
		for _, w := range []int{80, 100, 120} {
			bare, ticked := bareCredentials(true, sizes...), bareCredentials(false, sizes...)
			bl, tl := renderAt(t, bare, 1, w, 24), renderAt(t, ticked, 1, w, 24)
			y := slices.IndexFunc(bl, func(l string) bool { return strings.Contains(l, "credentials") })
			if y < 0 {
				t.Fatalf("%d columns: no credentials row:\n%s", w, strings.Join(bl, "\n"))
			}
			for j, opt := range bare.Rows[0].Options {
				whole := fmt.Sprintf("%s (%d)", opt, sizes[j])
				if sizes[j] > 0 {
					whole += " ▾"
				}
				if got := cellAt(bl, bare, y, j, w); got != whole {
					t.Errorf("%d columns, %v: bare cell %q = %q, want %q whole", w, sizes, opt, got, whole)
				}
				tick := map[bool]string{true: "[x] ", false: "[ ] "}[sizes[j] > 0]
				if got := cellAt(tl, ticked, y, j, w); !strings.HasPrefix(got, tick) {
					t.Errorf("%d columns, %v: unflagged cell %q = %q lost its tick box %q", w, sizes, opt, got, tick)
				}
			}
			if strings.Contains(bl[y], "[x]") || strings.Contains(bl[y], "[ ]") {
				t.Errorf("%d columns: the bare row drew a tick box: %q", w, bl[y])
			}
		}
	}
}

// Bare changes the glyph alone: each cell keeps the style its ticked twin draws, at rest and under
// the cursor, on a locked row and on a row the operator ticks; the cursor fill spans the cell's
// text and stops after its arrow.
func TestABareRowKeepsItsStateStylesAndCursorFill(t *testing.T) {
	t.Parallel()
	p := styles()
	draw := func(bare, locked bool, j, cursor int) (*screen, int, string) {
		f := bareCredentials(bare, 4, 0, 1, 20)
		f.Rows[0].Locked = locked
		f.Rows[0].point(j)
		s := simScreen(t, 80, 24)
		f.draw(s, cursor, 0)
		lines := screenLines(s)
		y := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "credentials") })
		return s, y, cellAt(lines, f, y, j, 80)
	}
	atRest := map[[2]bool]struct {
		name string
		st   lipgloss.Style
	}{
		{true, true}: {"held", p.fixed}, {true, false}: {"gated", p.gated},
		{false, true}: {"chosen", p.chosen}, {false, false}: {"offered", p.offered},
	}
	for _, locked := range []bool{true, false} {
		for j := range 4 {
			for _, cursor := range []int{0, 1} {
				bs, y, text := draw(true, locked, j, cursor)
				ts, _, _ := draw(false, locked, j, cursor)
				at := optOrigin + j*pitch(80, 3)
				_, _, want, _ := ts.GetContent(at, y)
				for x := at; x < at+textWidth(text); x++ {
					if _, _, got, _ := bs.GetContent(x, y); !sameStyle(got, want) {
						t.Errorf("locked %v, cell %d, cursor row %d: %q column %d drawn %q, its ticked twin %q",
							locked, j, cursor, text, x-at, got.Render("x"), want.Render("x"))
						break
					}
				}
				if _, _, past, _ := bs.GetContent(at+textWidth(text), y); cursor == 0 && sameStyle(past, want) {
					t.Errorf("locked %v, cell %d: the cursor fill runs past %q", locked, j, text)
				}
				if rest := atRest[[2]bool{locked, j != 1}]; cursor == 1 && !sameStyle(want, rest.st) {
					t.Errorf("locked %v, cell %d at rest drawn %q, want %s", locked, j, want.Render("x"), rest.name)
				}
			}
		}
	}
}

// A Bare row without grid columns packs its cells three columns apart, the tick box's four
// columns gone from every pitch.
func TestABareRowPacksItsCellsWithoutTickBoxes(t *testing.T) {
	t.Parallel()
	for bare, want := range map[bool]string{true: "alpha   beta   gamma", false: "[x] alpha   [ ] beta   [ ] gamma"} {
		f := &Form{Title: "t", Rows: []Row{{Label: "r", Multi: true, Bare: bare, Options: []string{"alpha", "beta", "gamma"}, On: []bool{true, false, false}}}}
		lines := renderAt(t, f, 0, 80, 12)
		if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "› r") && strings.Contains(l, want) }) {
			t.Errorf("bare %v: the row lacks %q:\n%s", bare, want, strings.Join(lines, "\n"))
		}
	}
}

// Every cell of a Bare locked row takes the cursor, and the help names it with its status as on
// the ticked row; the help slot holds the same lines.
func TestABareRowHoversEveryCellAndNamesItsStatus(t *testing.T) {
	t.Parallel()
	bare, ticked := bareCredentials(true, 4, 0, 1, 20), bareCredentials(false, 4, 0, 1, 20)
	keys := []string{"right", "right", "right", "right", "left"}
	got, want := bare.Walk("credentials", keys...), ticked.Walk("credentials", keys...)
	var seen []string
	for i, hv := range got {
		seen = append(seen, hv.Option)
		if !slices.Equal(hv.Help, want[i].Help) || !slices.Equal(hv.Status, want[i].Status) || hv.Option != want[i].Option {
			t.Errorf("press %d: bare row %+v, ticked row %+v", i, hv, want[i])
		}
		name := "› " + hv.Option
		if bare.Rows[0].Lists[slices.Index(bare.Rows[0].Options, hv.Option)] != nil {
			name = "› entry-000 — "
		}
		if len(hv.Help) == 0 || !strings.HasPrefix(hv.Help[0], name) || len(hv.Status) == 0 {
			t.Errorf("press %d on %q: help %q status %q; want %q and a status", i, hv.Option, hv.Help, hv.Status, name)
		}
	}
	slices.Sort(seen)
	if !slices.Equal(slices.Compact(seen), []string{".env", "API keys", "OAuth", "SSH"}) {
		t.Errorf("the cursor landed on %q, want every cell", seen)
	}
	if b, tk := bare.maxHelpLines(helpWidth(80)), ticked.maxHelpLines(helpWidth(80)); b != tk {
		t.Errorf("help slot: bare %d lines, ticked %d", b, tk)
	}
}

// A cell with no tick box gives its four columns to the words: "API keys (4) ▾" fits 14 columns
// whole, and a two-digit count cuts the words first.
func TestABareCellGivesTheTickBoxColumnsToItsWords(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		cell string
		room int
		want string
	}{
		{"API keys (4)", 14, "API keys (4) ▾"},
		{"API keys (12)", 14, "API ke… (12) ▾"},
		{"OAuth (2)", 14, "OAuth (2) ▾"},
		{".env (20)", 12, ".env (20) ▾"},
		{".env (123)", 12, ".env (123) ▾"},
		{".env (1234)", 12, ".e… (1234) ▾"},
	} {
		if got := fitCell("", c.cell, " ▾", c.room); got != c.want {
			t.Errorf("fitCell(%q, %d) = %q, want %q", c.cell, c.room, got, c.want)
		}
	}
}
