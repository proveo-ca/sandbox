// SPEC: _spec/internal/choiceui/wireframe.puml
package choiceui

import (
	"strings"
	"testing"
)

func helpText(r Row, width int) string {
	var b strings.Builder
	for _, l := range r.helpLines(width) {
		b.WriteString(l.text + "\n")
	}
	return b.String()
}

// A single-select row can never put the cursor on a gated option — cycle() skips
// it, correctly, because it cannot be chosen — so the OffWhy hung on that option
// is text nothing will ever display. The row-level Reason is the only place its
// explanation can appear, and it was suppressed the moment the row carried any
// Help at all: the auth row drew "usage credits" greyed out on cursor and said
// nothing whatsoever about why, which is the one thing drawing it is for.
func TestAGatedOptionOnASingleSelectRowStillExplainsItself(t *testing.T) {
	t.Parallel()
	r := Row{
		Label:    "auth",
		Options:  []string{"usage credits", "subscription"},
		Selected: 1,
		Off:      []bool{true, false},
		OffWhy:   map[string]string{"usage credits": "this CLI has no bring-your-own-key path"},
		Reason:   "usage credits: this CLI has no bring-your-own-key path",
		Help: map[string]string{
			"usage credits": "metered per token",
			"subscription":  "billed against the plan",
		},
	}
	got := helpText(r, 80)
	if !strings.Contains(got, "bring-your-own-key") {
		t.Errorf("the gated option is drawn with no reachable explanation:\n%s", got)
	}
	if !strings.Contains(got, "billed against the plan") {
		t.Errorf("the selected option lost its own help:\n%s", got)
	}
}

// With nothing gated there is no unreachable text, so the row must not start
// repeating a Reason beside the help of whatever the cursor is on.
func TestAnUngatedRowDoesNotRepeatItsReason(t *testing.T) {
	t.Parallel()
	r := Row{
		Label:    "auth",
		Options:  []string{"usage credits", "subscription"},
		Selected: 0,
		Off:      []bool{false, false},
		Reason:   "should not appear",
		Help:     map[string]string{"usage credits": "metered per token"},
	}
	if got := helpText(r, 80); strings.Contains(got, "should not appear") {
		t.Errorf("a row with nothing gated printed its Reason anyway:\n%s", got)
	}
}

// A Multi row CAN hover a gated option (cycle moves there, toggle refuses), so
// its OffWhy is reachable and the row-level Reason must stay as it was — the
// add-on rows carry several reasons at once and would double up.
func TestAMultiRowKeepsItsExistingReasonBehaviour(t *testing.T) {
	t.Parallel()
	r := Row{
		Label:    "execution",
		Options:  []string{"host", "docker (sandbox)"},
		Selected: 1,
		Multi:    true,
		On:       []bool{false, true},
		Off:      []bool{true, true},
		OffWhy:   map[string]string{"docker (sandbox)": "this harness runs in the sandbox"},
		Reason:   "docker sandbox: PROVEO_SBX=0 is set",
		Help:     map[string]string{"docker (sandbox)": "a microVM with its own Docker daemon"},
	}
	got := helpText(r, 80)
	if !strings.Contains(got, "this harness runs in the sandbox") {
		t.Errorf("a hovered gated option lost its OffWhy:\n%s", got)
	}
	if strings.Contains(got, "PROVEO_SBX=0") {
		t.Errorf("the Multi row now doubles its reason up with the OffWhy:\n%s", got)
	}
}

// The help slot is reserved from maxHelpLines, so the extra line has to be
// counted or the reason is drawn into rows the layout gave to something else.
func TestTheReservedHelpSlotCountsTheGatedReason(t *testing.T) {
	t.Parallel()
	f := &Form{Rows: []Row{{
		Label:    "auth",
		Options:  []string{"usage credits", "subscription"},
		Selected: 1,
		Off:      []bool{true, false},
		Reason:   "usage credits: this CLI has no bring-your-own-key path at all",
		Help:     map[string]string{"subscription": "billed against the plan the vendor issues"},
	}}}
	want := len(f.Rows[0].helpLines(76))
	if got := f.maxHelpLines(76); got < want {
		t.Errorf("reserved %d help lines for a row that draws %d", got, want)
	}
}
