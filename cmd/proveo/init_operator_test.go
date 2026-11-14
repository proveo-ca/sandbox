// SPEC: _spec/cmd/proveo/operator-name.puml
package main

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/operator"
)

func TestOperatorRowShowsTheNameInUse(t *testing.T) {
	if r := operatorRow(""); r.Placeholder != operator.Default || r.Held || !r.Field {
		t.Errorf("nothing stored ⇒ %+v; want a field showing %s", r, operator.Default)
	}
	if r := operatorRow("Roberto"); r.Placeholder != "Roberto" || !r.Held {
		t.Errorf("stored ⇒ %+v; want the stored name, held", r)
	}
	if !strings.Contains(operatorRow("").Reason, "16") {
		t.Error("the row should say only the first 16 characters are kept")
	}
}

func TestOperatorAnswerKeepsDefaultsAndTruncates(t *testing.T) {
	root := t.TempDir()
	if got, err := applyOperatorAnswer(root, "", "   "); err != nil || got != operator.Default {
		t.Errorf("first run, empty ⇒ %q, %v; want %s", got, err, operator.Default)
	}
	if stored, _ := operator.Load(root); stored != "" {
		t.Errorf("an empty first answer stored %q", stored)
	}
	if got, err := applyOperatorAnswer(root, "", "Roberto von Schoettler"); err != nil || got != "Roberto von Scho" {
		t.Errorf("typed ⇒ %q, %v; want the first 16 characters", got, err)
	}
	if got, err := applyOperatorAnswer(root, "Roberto von Scho", ""); err != nil || got != "Roberto von Scho" {
		t.Errorf("re-run, empty ⇒ %q, %v; want the stored name kept", got, err)
	}
	if stored, _ := operator.Load(root); stored != "Roberto von Scho" {
		t.Errorf("stored = %q", stored)
	}
}

// Page 4 has no Skip: Enter with the field empty keeps the name in use, and the help under the card
// is the field's Reason.
func TestTheOperatorPageHasNoSkipAndItsHelpIsTheReason(t *testing.T) {
	f := operatorForm(initStepAt(4), "", nil, setupAhead(false))
	if f.Skip != "" {
		t.Errorf("page 4 draws a %q button; Enter with the field empty keeps the name", f.Skip)
	}
	f.Glyphs = choiceui.GlyphsNerd
	if frame := f.Render(80, 24); !strings.Contains(frame, "› name — empty keeps "+operator.Default+"; the first 16 characters are kept") ||
		strings.Contains(frame, "[ Skip ]") {
		t.Errorf("page 4's help is not the field's Reason, or it draws Skip:\n%s", frame)
	}
}
