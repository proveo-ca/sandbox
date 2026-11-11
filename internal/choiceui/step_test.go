// SPEC: _spec/internal/choiceui/wireframe.puml
package choiceui

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/ui"
)

func stepForm(at, of int) *Form {
	return &Form{
		Title: "step",
		Step:  Step{At: at, Of: of},
		Rows:  []Row{{Label: "a", Options: []string{"x", "y"}}, {Label: "b", Options: []string{"x", "y"}}},
	}
}

func TestShiftTabStepsBackOnlyPastTheFirstScreen(t *testing.T) {
	t.Parallel()
	backtab := named("shift+tab")
	for _, tc := range []struct {
		step Step
		want Outcome
		done bool
	}{
		{Step{At: 2, Of: 3}, Back, true},
		{Step{At: 3, Of: 3}, Back, true},
		{Step{At: 1, Of: 3}, Cancelled, false},
		{Step{}, Cancelled, false},
	} {
		f := &Form{Step: tc.step}
		if got, done := f.leave(backtab); got != tc.want || done != tc.done {
			t.Errorf("step %+v: shift+tab = (%v, %v), want (%v, %v)", tc.step, got, done, tc.want, tc.done)
		}
	}
	f := &Form{Step: Step{At: 2, Of: 3}}
	if got, done := f.leave(named("enter")); got != Accepted || !done {
		t.Errorf("enter = (%v, %v), want Accepted", got, done)
	}
	if got, done := f.leave(named("esc")); got != Cancelled || !done {
		t.Errorf("esc = (%v, %v), want Cancelled", got, done)
	}
	if _, done := f.leave(key('q')); done {
		t.Error("q ended the form before a field could take it as typing")
	}
}

func TestHintNamesThePageTheWayBackAndTheScrollKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		at, of     int
		want, none []string
	}{
		{2, 3, []string{"page 2/3", "shift+tab back", "pgup/pgdn scroll", "enter accept"}, nil},
		{1, 3, []string{"page 1/3", "pgup/pgdn scroll", "enter accept"}, []string{"shift+tab back"}},
		{0, 0, []string{"pgup/pgdn scroll", "enter accept"}, []string{"page ", "shift+tab back"}},
	} {
		hint := stepForm(tc.at, tc.of).hint(200, 0)
		for _, w := range tc.want {
			if !strings.Contains(hint, w) {
				t.Errorf("step %d/%d: hint %q lacks %q", tc.at, tc.of, hint, w)
			}
		}
		for _, n := range tc.none {
			if strings.Contains(hint, n) {
				t.Errorf("step %d/%d: hint %q carries %q", tc.at, tc.of, hint, n)
			}
		}
	}
}

func TestANarrowHintDropsTheScrollKeysBeforeTheWayOut(t *testing.T) {
	t.Parallel()
	f := stepForm(2, 3)
	full := f.hint(200, 3)
	w := len([]rune(full)) - 1
	hint := f.hint(w, 3)
	if strings.Contains(hint, hintScroll) {
		t.Errorf("width %d kept %q over the way out: %q", w, hintScroll, hint)
	}
	for _, keep := range []string{"shift+tab back", "enter accept", "esc cancel", "3 more below"} {
		if !strings.Contains(hint, keep) {
			t.Errorf("width %d: hint %q lost %q", w, hint, keep)
		}
	}
}

// e2e waits on "enter accept"; no width that can hold it may lose it.
func TestEnterAcceptSurvivesEveryWidthThatCanHoldIt(t *testing.T) {
	t.Parallel()
	multi := stepForm(2, 3)
	multi.Rows = append(multi.Rows, Row{Label: "m", Options: []string{"x"}, Multi: true})
	for _, f := range []*Form{stepForm(2, 3), stepForm(1, 3), stepForm(0, 0), multi} {
		for w := len("enter accept"); w <= 160; w++ {
			if hint := f.hint(w, 7); !strings.Contains(hint, "enter accept") {
				t.Fatalf("step %+v width %d: %q lost \"enter accept\"", f.Step, w, hint)
			}
		}
	}
}

func TestTheDrawnFooterCarriesThePage(t *testing.T) {
	t.Parallel()
	if out := joined(t, stepForm(2, 3)); !strings.Contains(out, "page 2/3") || !strings.Contains(out, "enter accept") {
		t.Errorf("the footer does not name the page:\n%s", out)
	}
}

// The key line is two-tone: a key is bold and bright, the word beside it grey.
func TestTheKeyLinePaintsKeysBoldAndWordsGrey(t *testing.T) {
	t.Parallel()
	f := stepForm(2, 3)
	s := simScreen(t, 120, 30)
	f.draw(s, 0, 0)
	lines := screenLines(s)
	row := -1
	for y, l := range lines {
		if strings.Contains(l, "enter accept") {
			row = y
		}
	}
	if row < 0 {
		t.Fatalf("no key line on the frame:\n%s", strings.Join(lines, "\n"))
	}
	if got, want := lines[row], f.hint(120, 0); got != want {
		t.Errorf("the painted key line %q is not its plain projection %q", got, want)
	}
	at := func(word string) int { return textWidth(lines[row][:strings.Index(lines[row], word)]) }
	for _, key := range []string{"enter", "esc", "shift+tab", "↑↓"} {
		if _, _, st, _ := s.GetContent(at(key), row); !st.GetBold() || fgHex(st) != hexName(ui.ColorSecondary) {
			t.Errorf("the key %q is not bold and bright: %q", key, st.Render("x"))
		}
	}
	for _, word := range []string{"accept", "cancel", "back", "row", "page 2/3"} {
		if _, _, st, _ := s.GetContent(at(word), row); st.GetBold() || fgHex(st) != hexName(ui.ColorMuted) {
			t.Errorf("the word %q is not grey: %q", word, st.Render("x"))
		}
	}
}

// A key line too long for the screen is clipped where the plain line is, whichever segment that falls in.
func TestAClippedKeyLineStillEndsWhereThePlainLineEnds(t *testing.T) {
	t.Parallel()
	f := stepForm(0, 0)
	for _, w := range []int{4, 9, 13, 20, 31} {
		s := simScreen(t, w, 30)
		c := newCanvas(s)
		f.drawHint(c, styles(), w, 0)
		if got, want := screenLines(s)[0], strings.TrimRight(f.hint(w, 0), " "); got != want {
			t.Errorf("width %d: painted %q, plain %q", w, got, want)
		}
	}
}
