// SPEC: _spec/cmd/proveo/operator-name.puml
package main

import (
	"strings"
	"testing"

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
