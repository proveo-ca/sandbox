// SPEC: _spec/internal/sbx/kit-lifecycle.puml, _spec/defs/opencode/engine-local-history.puml, _spec/internal/choiceui/confirm-dialogs.puml
package choiceui

import (
	"fmt"
	"strings"
)

// SandboxUpgradeCopy explains replacing a kept sandbox so this run's settings apply.
// Every harness reuses one sandbox name, so the question is the same for each.
func SandboxUpgradeCopy(name string, changed []string, live, clone, history bool) []string {
	lines := []string{
		sandboxUpgradeTitle,
		strings.Join(changed, ", "),
		"",
		"1. carry clone commits and agent state home",
		"2. sbx rm --force " + name + " will run.",
		"3. this run creates a new sandbox with this same name",
		"",
		"This session does not persist.",
		"Host files under the proveo home stay.",
	}
	if clone {
		lines = append(lines, "Uncommitted clone edits are lost unless fetched.")
	} else {
		lines = append(lines, "Edits in the host checkout stay on disk.")
	}
	if history {
		lines = append(lines, "Its OpenCode history moves to the new sandbox.")
	}
	if live {
		lines = append(lines, "It is running. Yes replaces that live session.")
	}
	return lines
}

const sandboxUpgradeTitle = "this sandbox was created with different settings"

// AskSandboxUpgrade asks on the operator's terminal whether to replace a kept sandbox.
// asked is false when no terminal opens.
func AskSandboxUpgrade(name string, changed []string, live, clone, history bool) (yes, asked bool) {
	answer, asked := askDialog(sandboxUpgradeDialog(name, changed, live, clone, history))
	return asked && answer == 1, asked
}

func sandboxUpgradeDialog(name string, changed []string, live, clone, history bool) dialog {
	return newDialog(sandboxUpgradeTitle, SandboxUpgradeCopy(name, changed, live, clone, history), 0,
		dialogButton{Label: "Keep", Key: 'k'},
		dialogButton{Label: "Replace", Key: 'r', Danger: true},
	)
}

// UnansweredRemovalCopy explains removing a kept sandbox that gave no answer.
// It lists the refs sbx mirrored from it, since nothing else of it reaches the host.
func UnansweredRemovalCopy(name, mirror string, refs []string, history bool) []string {
	lines := []string{"it gave no answer, so its work cannot be carried home", ""}
	if len(refs) == 0 {
		lines = append(lines, "Nothing of it is mirrored under "+mirror+"/.")
	} else {
		lines = append(lines, "Mirrored on the host under "+mirror+"/:")
		for i, r := range refs {
			if i == maxListedRefs {
				lines = append(lines, fmt.Sprintf("  … and %d more", len(refs)-i))
				break
			}
			lines = append(lines, "  "+strings.TrimPrefix(r, mirror+"/"))
		}
	}
	lines = append(lines, "",
		"1. sbx rm --force "+name+" will run.",
		"2. this run creates a new sandbox with this same name",
		"",
		"Work newer than those refs is lost.",
	)
	if history {
		lines = append(lines, "Its OpenCode history is lost.")
	}
	return lines
}

const maxListedRefs = 6

// AskUnansweredRemoval asks on the operator's terminal whether to remove a sandbox that gave
// no answer. asked is false when no terminal opens.
func AskUnansweredRemoval(name, mirror string, refs []string, history bool) (yes, asked bool) {
	answer, asked := askDialog(unansweredRemovalDialog(name, mirror, refs, history))
	return asked && answer == 1, asked
}

func unansweredRemovalDialog(name, mirror string, refs []string, history bool) dialog {
	return newDialog(name+" gave no answer", UnansweredRemovalCopy(name, mirror, refs, history), 0,
		dialogButton{Label: "Keep", Key: 'k'},
		dialogButton{Label: "Remove", Key: 'r', Danger: true},
	)
}
