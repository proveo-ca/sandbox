// SPEC: _spec/defs/opencode/engine-local-history.puml
package choiceui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestTheUpgradePromptNamesTheConsequence(t *testing.T) {
	t.Parallel()
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(120, 40)
	drawModal(sim, SandboxUpgradeCopy("proveo-claudecode-abc", []string{"image", "kit"}, true, true, false), "this sandbox was created with different settings", upgradeChoices(), "")
	upgrade := rendered(t, sim)
	for _, want := range []string{
		"image, kit",
		"1. carry clone commits and agent state home",
		"2. sbx rm --force proveo-claudecode-abc will run.",
		"3. this run creates a new sandbox with this same name",
		"This session does not persist.",
		"It is running. Yes replaces that live session.",
		"Uncommitted clone edits are lost unless fetched.",
		"[y/N]",
		"replace the sandbox",
		"[esc]",
		"keep it — the next run reattaches",
		"enter submits",
	} {
		if !strings.Contains(upgrade, want) {
			t.Errorf("upgrade modal missing %q\n%s", want, upgrade)
		}
	}
	if strings.Contains(upgrade, "OpenCode history") {
		t.Errorf("a claudecode upgrade promises to move OpenCode history\n%s", upgrade)
	}
	sim.Clear()
	drawModal(sim, SandboxUpgradeCopy("proveo-opencode-abc", []string{"image"}, false, false, true), "this sandbox was created with different settings", upgradeChoices(), "")
	engine := rendered(t, sim)
	for _, want := range []string{"Edits in the host checkout stay on disk.", "Its OpenCode history moves to the new sandbox."} {
		if !strings.Contains(engine, want) {
			t.Errorf("OpenCode upgrade modal missing %q\n%s", want, engine)
		}
	}
}

func TestTheUpgradePromptKeys(t *testing.T) {
	t.Parallel()
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sim.Fini)
	sim.SetSize(100, 36)
	sim.InjectKey(tcell.KeyRune, 'n', tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	if ConfirmSandboxUpgrade(sim, "proveo-opencode-abc", []string{"image"}, false, false, true) {
		t.Fatal("n replaced the sandbox")
	}
	sim.InjectKey(tcell.KeyRune, 'y', tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	if !ConfirmSandboxUpgrade(sim, "proveo-opencode-abc", []string{"image"}, false, false, true) {
		t.Fatal("y did not replace the sandbox")
	}
}

func TestTheUnansweredRemovalPromptListsTheMirroredRefs(t *testing.T) {
	t.Parallel()
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(120, 40)
	mirror := "refs/sandboxes/proveo-claudecode-abc"
	refs := []string{mirror + "/main", mirror + "/a", mirror + "/b", mirror + "/c", mirror + "/d", mirror + "/e", mirror + "/f", mirror + "/g"}
	drawModal(sim, UnansweredRemovalCopy("proveo-claudecode-abc", mirror, refs, false), "proveo-claudecode-abc gave no answer", unansweredChoices(), "")
	got := rendered(t, sim)
	for _, want := range []string{
		"its work cannot be carried home",
		"Mirrored on the host under " + mirror + "/:",
		"  main",
		"… and 2 more",
		"1. sbx rm --force proveo-claudecode-abc will run.",
		"Work newer than those refs is lost.",
		"remove it anyway",
		"keep it — this run stops",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("unanswered modal missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "OpenCode history") {
		t.Errorf("a claudecode removal mentions OpenCode history\n%s", got)
	}
	none := UnansweredRemovalCopy("proveo-opencode-abc", "refs/sandboxes/proveo-opencode-abc", nil, true)
	joined := strings.Join(none, "\n")
	for _, want := range []string{"Nothing of it is mirrored", "Its OpenCode history is lost."} {
		if !strings.Contains(joined, want) {
			t.Errorf("unanswered copy without refs missing %q\n%s", want, joined)
		}
	}
}
