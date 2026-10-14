// SPEC: _spec/_plans/opencode-versioned-history-storage.puml
package choiceui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestBlockedRunCopyNamesTheConsequence(t *testing.T) {
	t.Parallel()
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(120, 40)
	drawPrompt(sim, SandboxResetCopy("proveo-opencode-11c4aed0"), "this sandbox cannot start")
	reset := rendered(t, sim)
	for _, want := range []string{
		"sbx rm --force proveo-opencode-11c4aed0 will run.",
		"This session does not persist.",
		"the next run reattaches",
	} {
		if !strings.Contains(reset, want) {
			t.Errorf("reset modal missing %q\n%s", want, reset)
		}
	}
	sim.Clear()
	drawPrompt(sim, EmptyPrepareCopy([]string{"opencode/share"}), "OpenCode V2 history is not prepared")
	prep := rendered(t, sim)
	for _, want := range []string{
		"1. initialize an empty V2 database",
		"2. publish a credential-free checkpoint",
		"3. start the agent on that cache",
		"opencode/share",
		"The older store is not imported.",
		"does not survive sbx rm",
	} {
		if !strings.Contains(prep, want) {
			t.Errorf("prepare modal missing %q\n%s", want, prep)
		}
	}
	sim.Clear()
	drawPrompt(sim, SandboxUpgradeCopy("proveo-claudecode-abc", []string{"image", "kit"}, true, true), "this sandbox was created with different settings")
	upgrade := rendered(t, sim)
	for _, want := range []string{
		"image, kit",
		"1. carry clone commits and agent state home",
		"2. sbx rm --force proveo-claudecode-abc will run.",
		"3. this run creates a new sandbox with this same name",
		"This session does not persist.",
		"It is running. Yes replaces that live session.",
		"Uncommitted clone edits are lost unless fetched.",
	} {
		if !strings.Contains(upgrade, want) {
			t.Errorf("upgrade modal missing %q\n%s", want, upgrade)
		}
	}
	sim.Clear()
	drawPrompt(sim, SandboxUpgradeCopy("proveo-codex-abc", []string{"image"}, false, false), "this sandbox was created with different settings")
	direct := rendered(t, sim)
	if !strings.Contains(direct, "Edits in the host checkout stay on disk.") {
		t.Errorf("direct checkout modal missing the host-edit line\n%s", direct)
	}
}

func TestBlockedRunKeys(t *testing.T) {
	t.Parallel()
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sim.Fini)
	sim.SetSize(100, 36)
	sim.InjectKey(tcell.KeyRune, 'n', tcell.ModNone)
	if ConfirmSandboxReset(sim, "proveo-opencode-11c4aed0") {
		t.Fatal("n removed the sandbox")
	}
	sim.InjectKey(tcell.KeyRune, 'y', tcell.ModNone)
	if !ConfirmEmptyPrepare(sim, []string{"opencode/share"}) {
		t.Fatal("y did not prepare")
	}
}

func TestRetainedRecoveryNamesEveryWayOut(t *testing.T) {
	t.Parallel()
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(120, 40)
	lines := RetainedRecoveryCopy("proveo-opencode-11c4aed0", "active")
	drawPrompt(sim, lines, lines[0])
	got := rendered(t, sim)
	for _, want := range []string{
		"ended before publishing",
		"Journal phase: active.",
		"y  recover",
		"r  reset — sbx rm --force proveo-opencode-11c4aed0,",
		"Sessions never published there are lost.",
		"the next run asks again",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("recovery modal missing %q\n%s", want, got)
		}
	}
}

func TestRetainedRecoveryKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		key  tcell.Key
		r    rune
		want RetainedRecovery
	}{
		{tcell.KeyRune, 'y', RetainedRecover},
		{tcell.KeyRune, 'r', RetainedReset},
		{tcell.KeyRune, 'n', RetainedStop},
		{tcell.KeyEscape, 0, RetainedStop},
		{tcell.KeyEnter, 0, RetainedStop},
	} {
		sim := tcell.NewSimulationScreen("UTF-8")
		if err := sim.Init(); err != nil {
			t.Fatal(err)
		}
		sim.SetSize(100, 36)
		sim.InjectKey(tc.key, tc.r, tcell.ModNone)
		if got := ChooseRetainedRecovery(sim, "proveo-opencode-11c4aed0", "active"); got != tc.want {
			t.Errorf("key %v %q chose %v, want %v", tc.key, tc.r, got, tc.want)
		}
		sim.Fini()
	}
}
