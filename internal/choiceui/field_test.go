package choiceui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

// SPEC: _spec/cmd/proveo/init-credential-wireframe.puml
func fieldForm() *Form {
	return &Form{
		Title: "credentials",
		Rows: []Row{
			{Label: "claudecode", Field: true, Masked: true, Held: true, Placeholder: "stored — type to replace"},
			{Label: "openai", Field: true, Masked: true, Placeholder: "empty — type a key"},
			{Label: "google", Field: true, Masked: true, Warn: true, Placeholder: "env only — Enter to import"},
		},
	}
}

func key(r rune) *tcell.EventKey { return tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone) }

func TestTypingReachesTheFieldAndNotTheNavigation(t *testing.T) {
	t.Parallel()
	f := fieldForm()
	for _, r := range "sk-qjkl " { // every one of these is also a command
		if !f.typing(1, key(r)) {
			t.Fatalf("%q was not taken by the field — it would have moved the cursor or cancelled the form", r)
		}
	}
	if got := f.FieldValue("openai"); got != "sk-qjkl " {
		t.Errorf("field holds %q, want every keystroke including the ones that are commands elsewhere", got)
	}
}

func TestBackspaceAndClear(t *testing.T) {
	t.Parallel()
	f := fieldForm()
	for _, r := range "abc" {
		f.typing(1, key(r))
	}
	f.typing(1, tcell.NewEventKey(tcell.KeyBackspace2, 0, tcell.ModNone))
	if got := f.FieldValue("openai"); got != "ab" {
		t.Errorf("after backspace: %q, want %q", got, "ab")
	}
	f.typing(1, tcell.NewEventKey(tcell.KeyCtrlU, 0, tcell.ModNone))
	if got := f.FieldValue("openai"); got != "" {
		t.Errorf("after ctrl-u: %q, want the field cleared", got)
	}
}

func TestEnterAndEscapeStayFormCommands(t *testing.T) {
	t.Parallel()
	f := fieldForm()
	for _, ev := range []*tcell.EventKey{
		tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone),
		tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone),
		tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone),
	} {
		if f.typing(1, ev) {
			t.Errorf("%v was swallowed by the field — accept, cancel and moving must still work while typing", ev.Key())
		}
	}
}

func TestATypedSecretIsNowhereOnTheScreen(t *testing.T) {
	t.Parallel()
	f := fieldForm()
	for _, r := range "sk-ant-secret" {
		f.typing(1, key(r))
	}
	screen := joined(t, f)
	if strings.Contains(screen, "sk-ant-secret") {
		t.Error("the typed key is drawn on the screen — a masked field is the only reason typing one here is safe")
	}
	if !strings.Contains(screen, strings.Repeat("•", len("sk-ant-secret"))) {
		t.Errorf("no dots for the typed runes; the operator cannot see that anything registered:\n%s", screen)
	}
}

func TestEachStateDrawsItsOwnPlaceholder(t *testing.T) {
	t.Parallel()
	screen := joined(t, fieldForm())
	for _, want := range []string{"stored — type to replace", "empty — type a key", "env only — Enter to import"} {
		if !strings.Contains(screen, want) {
			t.Errorf("placeholder %q is missing — an empty box says neither what it holds nor what typing would do:\n%s",
				want, screen)
		}
	}
}

func TestAHeldFieldNeverShowsWhatItHolds(t *testing.T) {
	t.Parallel()
	f := fieldForm()
	screen := joined(t, f)
	if !strings.Contains(screen, "✓") {
		t.Error("a held field draws no check — the operator cannot tell this host can already spend it")
	}
	if strings.Contains(screen, "claudecode") && strings.Contains(screen, "oauth") {
		t.Error("the stored value leaked into the screen; the store cannot hand it back and proveo must not print it")
	}
}
