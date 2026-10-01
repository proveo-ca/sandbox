// SPEC: _spec/cmd/proveo/operator-name.puml
package operator

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
		got := Sanitize(in)
		if got != want {
			t.Errorf("Sanitize(%q) = %q; want %q", in, got, want)
		}
		if utf8.RuneCountInString(got) > MaxRunes {
			t.Errorf("Sanitize(%q) kept %d runes", in, utf8.RuneCountInString(got))
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	if got, err := Load(root); err != nil || got != "" {
		t.Fatalf("nothing stored ⇒ %q, %v; want empty", got, err)
	}
	if got, err := Save(root, "  Roberto von Schoettler "); err != nil || got != "Roberto von Scho" {
		t.Fatalf("Save = %q, %v", got, err)
	}
	if got, _ := Load(root); got != "Roberto von Scho" {
		t.Errorf("Load = %q", got)
	}
	if !slices.Equal(Env(root), []string{EnvName + "=Roberto von Scho"}) {
		t.Errorf("Env = %v", Env(root))
	}
}

func TestSavingNothingRestoresTheDefault(t *testing.T) {
	root := t.TempDir()
	if _, err := Save(root, "Roberto"); err != nil {
		t.Fatal(err)
	}
	if got, err := Save(root, "  "); err != nil || got != "" {
		t.Fatalf("Save(empty) = %q, %v", got, err)
	}
	if _, err := os.Stat(Path(root)); !os.IsNotExist(err) {
		t.Errorf("operator.yml still exists after an empty answer: %v", err)
	}
	if Env(root) != nil {
		t.Errorf("Env = %v; want none, so the sandbox keeps %s", Env(root), Default)
	}
}
