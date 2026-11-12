// SPEC: _spec/internal/promptkit/prompt-kit.puml
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/promptkit"
	"github.com/proveo-ca/proveo/internal/run"
	"github.com/proveo-ca/proveo/internal/shell"
	"github.com/proveo-ca/proveo/internal/ui"
)

// promptKitOptions is the kit for this host: starship lands beside proveo's own binary. Its Report
// prints each line, which `proveo uninstall` keeps; init routes the kit to facts instead.
func promptKitOptions(ask bool) promptkit.Options {
	binDir := ""
	if exe, err := os.Executable(); err == nil {
		binDir = filepath.Dir(exe)
	}
	sh, _ := shell.Detect(os.Getenv("SHELL"))
	o := promptkit.Options{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Home: homeDir(), BinDir: binDir, StateDir: run.StateDir(),
		Shell: sh, Getenv: os.Getenv, Fetch: promptkit.HTTPFetch, LookPath: exec.LookPath,
		Preset: func(starship, name string) ([]byte, error) {
			return exec.Command(starship, "preset", name).Output()
		},
		Report: ui.Hostf,
	}
	if ask {
		o.DrawsGlyphs = choiceui.AskDrawsGlyphs
	}
	return o
}

// applyPromptKit installs the shell-prompt kit and prints its block under "prompt": starship, the
// font, the rc line and starship's config as facts, without what an earlier visit printed; a
// failure is a ⚠ and init goes on.
func applyPromptKit(ask bool, rep *initReport) {
	ui.Section(ui.SectionPrompt)
	var facts []ui.Fact
	var items []sandbox.Item
	o := promptKitOptions(ask)
	o.Report = nil
	o.Fact = func(key, value string, detail ...string) {
		for i := range detail {
			detail[i] = ui.TildeIn(detail[i])
		}
		facts = append(facts, ui.Fact{Role: ui.RoleApp, Key: key, Value: ui.TildeIn(value), Detail: detail})
	}
	o.Warn = func(text string, detail ...string) {
		items = append(items, sandbox.Item{Warn: true, Text: text, Detail: detail})
	}
	if _, err := promptkit.Apply(o); err != nil {
		items = append(items, sandbox.Item{Warn: true, Text: "prompt kit — not finished", Detail: errLines(err)})
	}
	rep.printFresh(promptHeading, ui.SectionPrompt, facts, items)
}
