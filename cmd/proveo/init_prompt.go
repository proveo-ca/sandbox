// SPEC: _spec/internal/promptkit/prompt-kit.puml
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/promptkit"
	"github.com/proveo-ca/proveo/internal/run"
	"github.com/proveo-ca/proveo/internal/shell"
	"github.com/proveo-ca/proveo/internal/ui"
)

// promptKitOptions is the kit for this host: starship lands beside proveo's own binary.
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

// applyPromptKit installs the shell-prompt kit; a failure is reported and init goes on.
func applyPromptKit(ask bool) {
	ui.Section(ui.SectionSetup)
	if _, err := promptkit.Apply(promptKitOptions(ask)); err != nil {
		ui.Warnf("the prompt kit was not finished (%v) — `proveo init` again retries it", err)
		return
	}
	ui.Notef("open a new shell to see it; `proveo uninstall` takes it out again")
}
