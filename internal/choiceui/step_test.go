// SPEC: _spec/internal/choiceui/wireframe.puml
package choiceui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
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
	backtab := tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModShift)
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
	if got, done := f.leave(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); got != Accepted || !done {
		t.Errorf("enter = (%v, %v), want Accepted", got, done)
	}
	if got, done := f.leave(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)); got != Cancelled || !done {
		t.Errorf("esc = (%v, %v), want Cancelled", got, done)
	}
	if _, done := f.leave(tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone)); done {
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
