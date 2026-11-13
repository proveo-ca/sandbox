// SPEC: _spec/internal/choiceui/confirm-dialogs.puml
package choiceui

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// plain is a view's text without styling.
func plain(s string) string { return sgr.ReplaceAllString(s, "") }

func pressKeys(m tea.Model, keys ...tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	for _, k := range keys {
		m, cmd = m.Update(k)
	}
	return m, cmd
}

func typed(s string) []tea.KeyPressMsg {
	var keys []tea.KeyPressMsg
	for _, r := range s {
		keys = append(keys, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return keys
}

var (
	teaEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	teaEsc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	teaRight = tea.KeyPressMsg{Code: tea.KeyRight}
	teaLeft  = tea.KeyPressMsg{Code: tea.KeyLeft}
	teaTab   = tea.KeyPressMsg{Code: tea.KeyTab}
	teaCtrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
)

// answered is the button a dialog accepted after keys; ended is false while it is still open.
func answered(t *testing.T, d tea.Model, keys ...tea.KeyPressMsg) (answer int, ended bool) {
	t.Helper()
	m, cmd := pressKeys(d, keys...)
	got, ok := dialogAnswer(m)
	if !ok {
		return -1, false
	}
	if cmd == nil {
		t.Fatalf("the dialog accepted %d and did not end", got.answer)
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		return got.answer, false
	}
	return got.answer, true
}

var upgradeArgs = struct {
	name    string
	changed []string
}{"proveo-claudecode-abc", []string{"image", "kit"}}

// theFiveDialogs are every confirmation proveo asks, with the index of its destructive or heavy answer.
func theFiveDialogs() map[string]dialog {
	mirror := "refs/sandboxes/proveo-claudecode-abc"
	return map[string]dialog{
		"close":      NewSandboxClose(nil, 30*time.Second).(dialog),
		"pushed":     fetchPushedDialog([]string{"origin/feat"}, 15*time.Second),
		"upgrade":    sandboxUpgradeDialog(upgradeArgs.name, upgradeArgs.changed, true, true, false),
		"unanswered": unansweredRemovalDialog("proveo-claudecode-abc", mirror, []string{mirror + "/main"}, false),
		"oversized":  oversizedModelDialog(qwenOnVRAM),
		"glyphs":     drawsGlyphsDialog(),
		"update":     agentUpdateDialog("claudecode", "2.1.312", "2.1.330", updateSteps),
	}
}

var updateSteps = []string{
	"docker pull proveo/claudecode-browser:latest (2.1.322)",
	"install 2.1.330 on top as registry.example:5000/team/claudecode-browser:agent-2.1.330",
}

func TestEnterAloneGivesTheSafeAnswerInEveryDialog(t *testing.T) {
	t.Parallel()
	for name, d := range theFiveDialogs() {
		for what, keys := range map[string][]tea.KeyPressMsg{
			"enter":          {teaEnter},
			"esc":            {teaEsc},
			"n then enter":   append(typed("n"), teaEnter),
			"right then esc": {teaRight, teaEsc},
		} {
			if got, ended := answered(t, d, keys...); !ended || got != d.safe {
				t.Errorf("%s, %s: answer %d (ended %v), want the safe answer %d", name, what, got, ended, d.safe)
			}
		}
		if d.safe != 0 || d.sel != 0 {
			t.Errorf("%s: the safe answer is button %d and button %d is selected; want the first for both", name, d.safe, d.sel)
		}
	}
}

func TestTheOtherAnswerTakesTwoKeys(t *testing.T) {
	t.Parallel()
	for name, d := range theFiveDialogs() {
		other := d.buttons[1]
		for what, keys := range map[string][]tea.KeyPressMsg{
			"its mnemonic then enter": append(typed(string(other.Key)), teaEnter),
			"right then enter":        {teaRight, teaEnter},
			"tab then enter":          {teaTab, teaEnter},
			"y then enter":            append(typed("y"), teaEnter),
		} {
			if got, _ := answered(t, d, keys...); got != 1 {
				t.Errorf("%s, %s: answer %d, want 1 (%s)", name, what, got, other.Label)
			}
		}
		for what, keys := range map[string][]tea.KeyPressMsg{
			"its mnemonic alone": typed(string(other.Key)),
			"y alone":            typed("y"),
			"right alone":        {teaRight},
		} {
			if got, ended := answered(t, d, keys...); ended || got != -1 {
				t.Errorf("%s, %s: the dialog ended with %d; one key must never give %s", name, what, got, other.Label)
			}
		}
	}
}

func TestTheSelectionWrapsAndTheEnterMarkFollowsIt(t *testing.T) {
	t.Parallel()
	d := NewSandboxClose(nil, 30*time.Second)
	if row := plain(d.(dialog).buttonRow()); row != " Stay ↵    Remove    Interrupt agent " {
		t.Errorf("button row = %q", row)
	}
	m, _ := pressKeys(d, teaRight)
	if row := plain(m.(dialog).buttonRow()); row != " Stay    Remove ↵    Interrupt agent " {
		t.Errorf("button row after right = %q", row)
	}
	m, _ = pressKeys(d, teaLeft)
	if got := m.(dialog).sel; got != 2 {
		t.Errorf("left from the first button selected %d, want the last", got)
	}
	m, _ = pressKeys(m, teaRight)
	if got := m.(dialog).sel; got != 0 {
		t.Errorf("right from the last button selected %d, want the first", got)
	}
}

func TestTheCloseDialogSaysWhatItDoesAndNamesItsKeys(t *testing.T) {
	t.Parallel()
	d := NewSandboxClose(nil, 30*time.Second).(dialog)
	d.w, d.h = 100, 30
	out := plain(d.View().Content)
	for _, want := range []string{
		"close this sandbox?",
		"sbx rm --force will run.",
		"That deletes the session.",
		"Uncommitted changes can be permanently lost.",
		"Stay ↵", "Remove", "Interrupt agent",
		"←/→ move · enter accept · esc stay · ctrl+c interrupt agent",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the close dialog lacks %q\n%s", want, out)
		}
	}
	if !d.View().AltScreen {
		t.Error("a dialog must draw on the alternate screen")
	}
	if got, ended := answered(t, d, teaCtrlC); !ended || SandboxClose(got) != SandboxClosePass {
		t.Errorf("ctrl+c answered %d, want the interrupt", got)
	}
	if got, _ := answered(t, d, teaTab, teaTab, teaEnter); SandboxClose(got) != SandboxClosePass {
		t.Errorf("the third button answered %d, want the interrupt", got)
	}
	for _, m := range []tea.Model{nil, d} {
		if got := SandboxCloseAnswer(m); got != SandboxCloseStay {
			t.Errorf("an unanswered dialog reads as %d, want stay", got)
		}
	}
}

func TestAcceptingRemoveStopsTheAgentOnceAndKeepsTheDialogUp(t *testing.T) {
	t.Parallel()
	stops := 0
	d := NewSandboxClose(func() { stops++ }, 30*time.Second)
	m, cmd := pressKeys(d, append(typed("r"), teaEnter)...)
	if stops != 1 {
		t.Fatalf("stop ran %d times, want once", stops)
	}
	if cmd == nil {
		t.Fatal("a held dialog needs its clock")
	}
	if _, quit := cmd().(tea.QuitMsg); quit {
		t.Fatal("the dialog closed while the agent was still stopping")
	}
	held := m.(dialog)
	held.w, held.h = 100, 30
	out := plain(held.View().Content)
	if !strings.Contains(out, "stopping the agent (up to 30s)") {
		t.Errorf("the held dialog does not say a stop is in flight\n%s", out)
	}
	if strings.Contains(out, "Remove") || strings.Contains(out, "enter accept") {
		t.Errorf("the held dialog still offers its buttons\n%s", out)
	}
	later, _ := held.Update(dialogTick{at: held.heldAt.Add(4 * time.Second)})
	if out := plain(later.(dialog).box()); !strings.Contains(out, "— 4s") {
		t.Errorf("the held dialog shows no elapsed time\n%s", out)
	}
	again, _ := pressKeys(later, teaEnter, teaEsc, teaCtrlC)
	if stops != 1 {
		t.Errorf("a key on the held dialog ran stop again: %d", stops)
	}
	if got := SandboxCloseAnswer(again); got != SandboxCloseRemove {
		t.Errorf("the held dialog reads as %d, want remove", got)
	}
}

func TestSilenceLeavesThePushedBranches(t *testing.T) {
	t.Parallel()
	d := fetchPushedDialog([]string{"origin/feat"}, 15*time.Second)
	if d.Init() == nil {
		t.Fatal("the pushed-branch dialog starts no timer")
	}
	m, cmd := d.Update(dialogTimeout{})
	got, ok := dialogAnswer(m)
	if !ok || got.answer != 0 || !got.timedOut || cmd == nil {
		t.Errorf("a timeout answered %d (ended %v), want leave", got.answer, ok)
	}
	if answer, _ := answered(t, d, append(typed("f"), teaEnter)...); answer != 1 {
		t.Errorf("f then enter answered %d, want fetch", answer)
	}
	if theFiveDialogs()["upgrade"].Init() != nil {
		t.Error("a dialog without a wait started a timer")
	}
	out := plain(d.box())
	for _, want := range []string{"the agent pushed these branches", "origin/feat", "15s with no answer leaves them", "Leave ↵", "Fetch"} {
		if !strings.Contains(out, want) {
			t.Errorf("the pushed-branch dialog lacks %q\n%s", want, out)
		}
	}
}

func TestTheUpgradeDialogNamesTheConsequence(t *testing.T) {
	t.Parallel()
	upgrade := plain(sandboxUpgradeDialog("proveo-claudecode-abc", []string{"image", "kit"}, true, true, false).box())
	for _, want := range []string{
		"image, kit",
		"1. carry clone commits and agent state home",
		"2. sbx rm --force proveo-claudecode-abc will run.",
		"3. this run creates a new sandbox with this same name",
		"This session does not persist.",
		"It is running. Yes replaces that live session.",
		"Uncommitted clone edits are lost unless fetched.",
		"Keep ↵", "Replace", "esc keep",
	} {
		if !strings.Contains(upgrade, want) {
			t.Errorf("the upgrade dialog lacks %q\n%s", want, upgrade)
		}
	}
	if strings.Contains(upgrade, "OpenCode history") {
		t.Errorf("a claudecode upgrade promises to move OpenCode history\n%s", upgrade)
	}
	engine := plain(sandboxUpgradeDialog("proveo-opencode-abc", []string{"image"}, false, false, true).box())
	for _, want := range []string{"Edits in the host checkout stay on disk.", "Its OpenCode history moves to the new sandbox."} {
		if !strings.Contains(engine, want) {
			t.Errorf("the OpenCode upgrade dialog lacks %q\n%s", want, engine)
		}
	}
}

// SPEC: _spec/_devops/agent-update-prompt.puml
func TestTheUpdateDialogNamesItsStepsAndTakesThreeAnswers(t *testing.T) {
	t.Parallel()
	d := agentUpdateDialog("claudecode", "2.1.312", "2.1.330", updateSteps)
	box := plain(d.box())
	for _, want := range []string{
		"a newer claudecode agent is available",
		"2.1.312 → 2.1.330",
		"1. docker pull proveo/claudecode-browser:latest (2.1.322)",
		"2. install 2.1.330 on top as",
		"   registry.example:5000/team/claudecode-browser:agent-2.1.330",
		"Later asks again in 24 hours.",
		"Skip waits for a release after 2.1.330.",
		"Later ↵", "Update", "Skip", "esc later",
	} {
		if !strings.Contains(box, want) {
			t.Errorf("the update dialog lacks %q\n%s", want, box)
		}
	}
	for keys, want := range map[string]AgentUpdateAnswer{"": AgentUpdateLater, "u": AgentUpdateNow, "y": AgentUpdateNow, "s": AgentUpdateSkip, "l": AgentUpdateLater} {
		if got, ended := answered(t, d, append(typed(keys), teaEnter)...); !ended || AgentUpdateAnswer(got) != want {
			t.Errorf("%q then enter: answer %d (ended %v), want %d", keys, got, ended, want)
		}
	}
	if got, ended := answered(t, d, teaEsc); !ended || AgentUpdateAnswer(got) != AgentUpdateLater {
		t.Errorf("esc: answer %d (ended %v), want Later", got, ended)
	}
}

func TestAskAgentUpdateSaysWhenNoTerminalTookTheQuestion(t *testing.T) {
	old := askDialog
	t.Cleanup(func() { askDialog = old })
	askDialog = func(d dialog) (int, bool) { return d.safe, false }
	if got, asked := AskAgentUpdate("codex", "0.1.0", "0.2.0", nil); asked || got != AgentUpdateLater {
		t.Errorf("no terminal: %d, asked %v; want Later and not asked", got, asked)
	}
	askDialog = func(dialog) (int, bool) { return int(AgentUpdateSkip), true }
	if got, asked := AskAgentUpdate("codex", "0.1.0", "0.2.0", nil); !asked || got != AgentUpdateSkip {
		t.Errorf("answered Skip: %d, asked %v", got, asked)
	}
}

func TestTheUnansweredRemovalDialogListsTheMirroredRefs(t *testing.T) {
	t.Parallel()
	mirror := "refs/sandboxes/proveo-claudecode-abc"
	refs := []string{mirror + "/main", mirror + "/a", mirror + "/b", mirror + "/c", mirror + "/d", mirror + "/e", mirror + "/f", mirror + "/g"}
	got := plain(unansweredRemovalDialog("proveo-claudecode-abc", mirror, refs, false).box())
	for _, want := range []string{
		"its work cannot be carried home",
		"Mirrored on the host under " + mirror + "/:",
		"  main",
		"… and 2 more",
		"1. sbx rm --force proveo-claudecode-abc will run.",
		"Work newer than those refs is lost.",
		"Keep ↵", "Remove",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the unanswered dialog lacks %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "OpenCode history") {
		t.Errorf("a claudecode removal mentions OpenCode history\n%s", got)
	}
	none := strings.Join(UnansweredRemovalCopy("proveo-opencode-abc", "refs/sandboxes/proveo-opencode-abc", nil, true), "\n")
	for _, want := range []string{"Nothing of it is mirrored", "Its OpenCode history is lost."} {
		if !strings.Contains(none, want) {
			t.Errorf("unanswered copy without refs lacks %q\n%s", want, none)
		}
	}
}

func TestEveryDialogFitsAnEightyColumnTerminal(t *testing.T) {
	t.Parallel()
	for name, d := range theFiveDialogs() {
		for _, row := range strings.Split(plain(d.box()), "\n") {
			if w := textWidth(row); w > 80 {
				t.Errorf("%s: a row is %d columns wide: %q", name, w, row)
			}
		}
	}
}

func TestTheGlyphProbeDrawsThePresetsBranchAndLockAndCountsAnythingElseAsNo(t *testing.T) {
	t.Parallel()
	d := drawsGlyphsDialog()
	box := plain(d.box())
	for _, want := range []string{"\uf418  main", "\U000f033e  read-only", "Two icons mean", "a line, a blank"} {
		if !strings.Contains(box, want) {
			t.Errorf("the probe lacks %q:\n%s", want, box)
		}
	}
	if strings.Contains(box, "\ue0a0") {
		t.Errorf("the probe draws U+E0A0, a Powerline glyph the nerd-font-symbols preset never uses:\n%s", box)
	}
	if d.buttons[d.safe].Label != "Something else" || d.buttons[1].Label != "Two icons" {
		t.Errorf("buttons = %+v, want the safe answer \"Something else\" first", d.buttons)
	}
	rows := strings.Split(box, "\n")
	for _, row := range rows {
		if textWidth(row) != textWidth(rows[0]) {
			t.Errorf("a row is %d columns wide, the top edge %d: %q", textWidth(row), textWidth(rows[0]), row)
		}
	}
}

func TestADangerousAnswerDrawsItsMnemonicInTheFailureColour(t *testing.T) {
	t.Parallel()
	d := NewSandboxClose(nil, time.Second).(dialog)
	remove, stay := d.button(d.buttons[1], false), d.button(d.buttons[0], false)
	const failure = "203;32;0"
	if !strings.Contains(remove, failure) {
		t.Errorf("Remove's mnemonic is not drawn in the failure colour: %q", remove)
	}
	if strings.Contains(stay, failure) {
		t.Errorf("Stay is drawn in the failure colour: %q", stay)
	}
}

// Every button face is a reversed fill, so a face survives NO_COLOR; a danger mnemonic's face is the failure colour.
func TestEveryButtonFaceIsAReversedFill(t *testing.T) {
	t.Parallel()
	d := NewSandboxClose(nil, time.Second).(dialog)
	for _, b := range d.buttons {
		for _, selected := range []bool{false, true} {
			face := d.button(b, selected)
			for _, seq := range sgr.FindAllString(face, -1) {
				params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m"), ";")
				if len(params) == 1 && params[0] == "" {
					continue
				}
				if fg := slices.Index(params, "38"); fg < 0 || !slices.Contains(params[:fg], "7") {
					t.Errorf("%s (selected %v): %q is not a reversed fill", b.Label, selected, seq)
				}
			}
			if b.Danger && !strings.Contains(face, "7;38;2;203;32;0;") {
				t.Errorf("%s (selected %v): the mnemonic's face is not the failure colour: %q", b.Label, selected, face)
			}
		}
	}
}
