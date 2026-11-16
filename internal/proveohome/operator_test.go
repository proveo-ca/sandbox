// SPEC: _spec/cmd/proveo/operator-name.puml
package proveohome

import (
	"os"
	"slices"
	"testing"
	"unicode/utf8"
)

func TestSanitizeKeepsSixteenPrintableRunes(t *testing.T) {
	for in, want := range map[string]string{
		"  Roberto  ":                        "Roberto",
		"Supercalifragilisticexpialidocious": "Supercalifragili",
		"Señora Ñandú Pérez García":          "Señora Ñandú Pér",
		`Rob "the" \Exec\`:                   "Rob the Exec",
		"line\nbreak\tand\x00nul":            "linebreakandnul",
		"   ":                                "",
	} {
		got := SanitizeOperator(in)
		if got != want {
			t.Errorf("SanitizeOperator(%q) = %q; want %q", in, got, want)
		}
		if utf8.RuneCountInString(got) > OperatorMaxRunes {
			t.Errorf("SanitizeOperator(%q) kept %d runes", in, utf8.RuneCountInString(got))
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	if got, err := LoadOperator(root); err != nil || got != "" {
		t.Fatalf("nothing stored ⇒ %q, %v; want empty", got, err)
	}
	if got, err := SaveOperator(root, "  Roberto von Schoettler "); err != nil || got != "Roberto von Scho" {
		t.Fatalf("SaveOperator = %q, %v", got, err)
	}
	if got, _ := LoadOperator(root); got != "Roberto von Scho" {
		t.Errorf("LoadOperator = %q", got)
	}
	if !slices.Equal(OperatorEnv(root), []string{EnvOperatorName + "=Roberto von Scho"}) {
		t.Errorf("OperatorEnv = %v", OperatorEnv(root))
	}
}

func TestSavingNothingRestoresTheDefault(t *testing.T) {
	root := t.TempDir()
	if _, err := SaveOperator(root, "Roberto"); err != nil {
		t.Fatal(err)
	}
	if got, err := SaveOperator(root, "  "); err != nil || got != "" {
		t.Fatalf("SaveOperator(empty) = %q, %v", got, err)
	}
	if _, err := os.Stat(OperatorPath(root)); !os.IsNotExist(err) {
		t.Errorf("operator.yml still exists after an empty answer: %v", err)
	}
	if OperatorEnv(root) != nil {
		t.Errorf("OperatorEnv = %v; want none, so the sandbox keeps %s", OperatorEnv(root), DefaultOperator)
	}
}
