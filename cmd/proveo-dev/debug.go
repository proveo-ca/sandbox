// SPEC: _spec/cmd/proveo/provision-and-targets.puml, _spec/internal/maintain/image-build-deploy.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	fuzzyfinder "github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/maintain"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/ui"
)

const debugDefaultTag = "latest"

type debugArgs struct {
	Target string
	Tag    string
	Extra  []string
	Print  bool
	Help   bool
}

func parseDebugArgs(args []string) (debugArgs, error) {
	d := debugArgs{Tag: debugDefaultTag}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--tag":
			if i+1 >= len(args) {
				return d, errors.New("--tag requires a value")
			}
			d.Tag = args[i+1]
			i++
		case a == "--":
			d.Extra = append(d.Extra, args[i+1:]...)
			return d, nil
		case a == "--print":
			d.Print = true
		case a == "-h" || a == "--help":
			d.Help = true
		case strings.HasPrefix(a, "-"):
			return d, fmt.Errorf("unknown debug option: %s", a)
		case d.Target == "":
			d.Target = a
		default:
			return d, fmt.Errorf("multiple debug targets specified: %s and %s", d.Target, a)
		}
	}
	return d, nil
}

// debugArgv is the proveo argv that opens a shell in target's harness.
func debugArgv(t maintain.Target, tag string, extra []string) []string {
	argv := []string{"run", t.Name, "--shell"}
	if tag != "" && tag != debugDefaultTag {
		argv = append(argv, "--image", t.Image+":"+tag)
	}
	return append(argv, extra...)
}

// findProveo is the first executable proveo in dirs, or "" when none is.
func findProveo(dirs []string, isExec func(string) bool) string {
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if p := filepath.Join(d, "proveo"); isExec(p) {
			return p
		}
	}
	return ""
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

func lookupTarget(reg []maintain.Target, name string) (maintain.Target, bool) {
	for _, t := range reg {
		if t.Name == name {
			return t, true
		}
	}
	return maintain.Target{}, false
}

func failTargets(reg []maintain.Target, format string, a ...any) error {
	ui.Failf(format, a...)
	ui.Notef("Available targets:")
	for _, t := range reg {
		ui.Notef("  - %s", t.Name)
	}
	return &exitError{code: 1}
}

func loadDevRegistry(root string) ([]maintain.Target, error) {
	defs := envOr("PROVEO_DEFS_DIR", filepath.Join(root, "defs"))
	ms, err := manifest.Load(defs)
	if err != nil {
		return nil, fmt.Errorf("loading manifests from %s: %w", defs, err)
	}
	reg := maintain.Registry(ms, defs)
	if len(reg) == 0 {
		return nil, fmt.Errorf("no targets in %s", defs)
	}
	return reg, nil
}

func pickDebugTarget(reg []maintain.Target) (string, error) {
	fmt.Fprintf(os.Stderr, "\nSelect debug target:\n")
	idx, err := fuzzyfinder.Find(reg, func(i int) string { return reg[i].Name },
		fuzzyfinder.WithPromptString("debug> "))
	if err != nil {
		return "", err
	}
	return reg[idx].Name, nil
}

func runDebug(cmd *cobra.Command, args []string) error {
	d, err := parseDebugArgs(args)
	if err != nil {
		ui.Failf("%v", err)
		return &exitError{code: 1}
	}
	if d.Help {
		return cmd.Help()
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	reg, err := loadDevRegistry(root)
	if err != nil {
		return err
	}
	if d.Target == "" {
		if !agentio.IsStdinTTY() {
			return failTargets(reg, "Missing target.")
		}
		d.Target, err = pickDebugTarget(reg)
		if errors.Is(err, fuzzyfinder.ErrAbort) {
			fmt.Fprintln(os.Stderr, "Debug cancelled.")
			return nil
		}
		if err != nil {
			return err
		}
	}
	if d.Tag == "" {
		ui.Failf("Command 'debug' received an empty tag.")
		return &exitError{code: 1}
	}
	t, ok := lookupTarget(reg, d.Target)
	if !ok {
		return failTargets(reg, "Unknown target: %s", d.Target)
	}

	gopath, _ := output(root, "go", "env", "GOPATH")
	gobin := ""
	if first := filepath.SplitList(gopath); len(first) > 0 {
		gobin = filepath.Join(first[0], "bin")
	}
	path := os.Getenv("PATH")
	if gobin != "" {
		path = gobin + string(os.PathListSeparator) + path
	}
	bin := findProveo(filepath.SplitList(path), isExecutable)
	argv := debugArgv(t, d.Tag, d.Extra)

	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	name, dir := bin, wd
	if bin == "" {
		name, dir, argv = "go", root, append([]string{"run", "./cmd/proveo"}, argv...)
	}
	if d.Print {
		fmt.Fprintln(cmd.OutOrStdout(), strings.Join(append([]string{name}, argv...), " "))
		return nil
	}
	return run(dir, []string{"PATH=" + path, "PROVEO_BIN=" + bin}, name, argv...)
}

func init() {
	register(&cobra.Command{
		Use:                "debug [target] [--tag T] [--print] [-- proveo run args...]",
		Short:              "Debug a container harness def (shell via proveo run --shell)",
		DisableFlagParsing: true,
		RunE:               runDebug,
	})
}
