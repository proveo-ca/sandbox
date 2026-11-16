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
)

// promptKitOptions is the kit for this host: starship lands beside proveo's own binary; draws asks
// what the terminal drew of two Nerd glyphs, and nil asks nothing.
func promptKitOptions(draws func() (icon, asked bool)) promptkit.Options {
	binDir := ""
	if exe, err := os.Executable(); err == nil {
		binDir = filepath.Dir(exe)
	}
	sh, _ := promptkit.DetectShell(os.Getenv("SHELL"))
	o := promptkit.Options{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Home: homeDir(), BinDir: binDir, StateDir: run.StateDir(),
		Shell: sh, Getenv: os.Getenv, Fetch: promptkit.HTTPFetch, LookPath: exec.LookPath,
		Preset: func(starship, name string) ([]byte, error) {
			return exec.Command(starship, "preset", name).Output()
		},
	}
	o.DrawsGlyphs = draws
	return o
}

// applyPromptKit installs the shell-prompt kit and says on the prompt kit row what needs the
// operator; a failure is a ! and init goes on. It returns the row's outcome.
func applyPromptKit(draws func() (icon, asked bool), rep *initReport) (choiceui.Mark, string) {
	var items []sandbox.Item
	o := promptKitOptions(draws)
	o.Fact = func(string, string, ...string) {}
	o.Warn = func(text string, detail ...string) {
		items = append(items, sandbox.Item{Warn: true, Text: text, Detail: detail})
	}
	if _, err := promptkit.Apply(o); err != nil {
		items = append(items, sandbox.Item{Warn: true, Text: rowPrompt + " — not finished", Detail: errLines(err)})
	}
	rep.say(rowPrompt, items...)
	if len(items) > 0 {
		return itemMark(items[0])
	}
	return choiceui.MarkDone, "installed"
}
