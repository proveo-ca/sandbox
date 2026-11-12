// SPEC: _spec/internal/choiceui/scope-tree.puml
package choiceui

import (
	"slices"
	"strings"
	"testing"
)

func monoTree() *Tree {
	return NewTree([]string{"packages/ui", "apps/web", "apps/api", "services/billing/worker", "tools"})
}

func paths(t *Tree) []string {
	var out []string
	for _, r := range t.Rows() {
		out = append(out, r.Path)
	}
	return out
}

func TestTheRootIsTheFirstRowAndTheCursorStartsOnIt(t *testing.T) {
	t.Parallel()
	tr := monoTree()
	want := []string{"", "apps", "apps/api", "apps/web", "packages", "packages/ui",
		"services", "services/billing", "services/billing/worker", "tools"}
	if got := paths(tr); !slices.Equal(got, want) {
		t.Errorf("rows = %q\nwant   %q", got, want)
	}
	if tr.At() != "" {
		t.Errorf("the cursor starts on %q, want the root", tr.At())
	}
	rows := tr.Rows()
	if !rows[0].Folder || rows[1].Depth != 1 || rows[2].Depth != 2 || !rows[2].Given || rows[1].Given {
		t.Errorf("rows carry the wrong shape: %+v", rows[:3])
	}
}

func TestATreeOfNothingIsTheRootAlone(t *testing.T) {
	t.Parallel()
	tr := NewTree(nil)
	if got := paths(tr); !slices.Equal(got, []string{""}) {
		t.Errorf("rows = %q", got)
	}
	tr.Move(3)
	tr.Fold()
	tr.SetFilter("x")
	if tr.At() != "" {
		t.Errorf("the cursor left the root of an empty tree: %q", tr.At())
	}
}

func TestTheCursorStopsAtEitherEnd(t *testing.T) {
	t.Parallel()
	tr := monoTree()
	tr.Move(-4)
	if tr.At() != "" {
		t.Errorf("moving up from the root reached %q", tr.At())
	}
	tr.Move(2)
	if tr.At() != "apps/api" {
		t.Errorf("two rows down is %q, want apps/api", tr.At())
	}
	tr.Move(99)
	if tr.At() != "tools" {
		t.Errorf("the far end is %q, want tools", tr.At())
	}
}

func TestTheCursorsFolderHoldsEveryPathBelowItAndTheRootHoldsAll(t *testing.T) {
	t.Parallel()
	tr := NewTree([]string{"app/x", "apps/web", "services/billing/worker", "tools"})
	held := func() []string {
		var out []string
		for _, r := range tr.Rows() {
			if tr.Holds(r.Path) {
				out = append(out, r.Path)
			}
		}
		return out
	}
	if got, want := held(), paths(tr)[1:]; !slices.Equal(got, want) {
		t.Errorf("the root holds %q, want every row but itself %q", got, want)
	}
	for at, want := range map[string][]string{
		"services": {"services/billing", "services/billing/worker"},
		"app":      {"app/x"},
		"tools":    nil,
	} {
		tr.Place(at)
		if got := held(); !slices.Equal(got, want) {
			t.Errorf("%s holds %q, want %q", at, got, want)
		}
	}
}

func TestFoldingHidesAFoldersRowsAndCountsThem(t *testing.T) {
	t.Parallel()
	tr := monoTree()
	tr.Place("apps")
	tr.Fold()
	if got := paths(tr); slices.Contains(got, "apps/web") || !slices.Contains(got, "apps") {
		t.Errorf("a folded folder still shows its rows: %q", got)
	}
	row := tr.Rows()[1]
	if !row.Folded || row.Hidden != 2 {
		t.Errorf("the folded row = %+v, want folded with 2 hidden", row)
	}
	tr.Unfold()
	if got := paths(tr); !slices.Contains(got, "apps/web") {
		t.Errorf("unfolding did not bring the rows back: %q", got)
	}
}

func TestFoldOnALeafOrAClosedFolderStepsToTheFolderAbove(t *testing.T) {
	t.Parallel()
	tr := monoTree()
	tr.Place("services/billing/worker")
	tr.Fold()
	if tr.At() != "services/billing" {
		t.Errorf("fold on a leaf moved to %q, want services/billing", tr.At())
	}
	tr.Fold()
	tr.Fold()
	if tr.At() != "services" {
		t.Errorf("fold on a closed folder moved to %q, want services", tr.At())
	}
	tr.Place("tools")
	tr.Fold()
	if tr.At() != "" {
		t.Errorf("fold on a top-level leaf moved to %q, want the root", tr.At())
	}
	tr.Fold()
	if got := paths(tr); len(got) < 5 {
		t.Errorf("the root folded: %q", got)
	}
}

func TestPlaceOpensTheFoldersAboveAndRefusesAnUnknownPath(t *testing.T) {
	t.Parallel()
	tr := monoTree()
	tr.Place("services")
	tr.Fold()
	tr.Place("services/billing/worker")
	if tr.At() != "services/billing/worker" || !slices.Contains(paths(tr), "services/billing/worker") {
		t.Errorf("Place left the cursor on %q with rows %q", tr.At(), paths(tr))
	}
	tr.Place("no/such/folder")
	if tr.At() != "" {
		t.Errorf("an unknown path left the cursor on %q, want the root", tr.At())
	}
}

func TestAFilterKeepsTheRootTheMatchesAndTheFoldersAboveThem(t *testing.T) {
	t.Parallel()
	tr := monoTree()
	tr.Place("apps")
	tr.Fold()
	tr.SetFilter("WEB")
	if got, want := paths(tr), []string{"", "apps", "apps/web"}; !slices.Equal(got, want) {
		t.Errorf("rows under the filter = %q, want %q", got, want)
	}
	if tr.Rows()[1].Folded {
		t.Error("a fold hid a match: the filter must see through it")
	}
	tr.SetFilter("zzz")
	if got := paths(tr); !slices.Equal(got, []string{""}) {
		t.Errorf("a filter with no match shows %q, want the root alone", got)
	}
	tr.SetFilter("")
	if got := paths(tr); slices.Contains(got, "apps/web") {
		t.Errorf("clearing the filter from the root forgot the fold: %q", got)
	}
}

func TestAFilterPutsTheCursorOnItsFirstMatch(t *testing.T) {
	t.Parallel()
	tr := monoTree()
	tr.Place("tools")
	tr.SetFilter("bill")
	if tr.At() != "services/billing" {
		t.Errorf("the cursor went to %q, want the first match services/billing", tr.At())
	}
	tr.SetFilter("billi")
	if tr.At() != "services/billing" {
		t.Errorf("a cursor on a match moved to %q", tr.At())
	}
	tr.SetFilter("billing/w")
	if tr.At() != "services/billing/worker" {
		t.Errorf("a cursor that stopped matching went to %q, want services/billing/worker", tr.At())
	}
	tr.SetFilter("nothing")
	if tr.At() != "" {
		t.Errorf("with no match the cursor is on %q, want the root", tr.At())
	}
	tr = monoTree()
	tr.SetFilter("a")
	if tr.At() != "apps" {
		t.Errorf("from the root the cursor went to %q, want the first match apps", tr.At())
	}
}

func TestClearingAFilterKeepsTheCursorAndOpensTheFoldersAboveIt(t *testing.T) {
	t.Parallel()
	tr := monoTree()
	tr.Place("services")
	tr.Fold()
	tr.SetFilter("worker")
	tr.SetFilter("")
	if tr.At() != "services/billing/worker" || !slices.Contains(paths(tr), "services/billing/worker") {
		t.Errorf("after the filter cleared the cursor is on %q with rows %q", tr.At(), paths(tr))
	}
}

func TestAPathGivenTwiceCountsOnce(t *testing.T) {
	t.Parallel()
	tr := NewTree([]string{"apps/web", "/apps/web/", "apps/api", ""})
	tr.Place("apps")
	tr.Fold()
	if row := tr.Rows()[1]; row.Hidden != 2 {
		t.Errorf("hidden = %d, want 2: %s", row.Hidden, strings.Join(paths(tr), " "))
	}
}
