// SPEC: _spec/internal/shell/setup-path.puml, _spec/internal/promptkit/prompt-kit.puml
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/envflag"
	"github.com/proveo-ca/proveo/internal/promptkit"
	"github.com/proveo-ca/proveo/internal/results"
	"github.com/proveo-ca/proveo/internal/run"
	"github.com/proveo-ca/proveo/internal/schedule"
	"github.com/proveo-ca/proveo/internal/shell"
	"github.com/proveo-ca/proveo/internal/ui"
)

const (
	installPathMarkerStart = "# Added by proveo install.sh"
	installPathMarkerEnd   = "# End added by proveo install.sh"
)

func uninstallCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the CDN-installed proveo binary and PATH markers",
		Long: `Remove the proveo install root (default ~/.proveo, or PROVEO_INSTALL_ROOT) and the
PATH lines install.sh and proveo setup wrote into the shell rc files.

Also removes what the prompt kit of proveo init added: its line in the shell rc,
its block in starship.toml, and the starship binary and Nerd Font files only
when proveo installed them.

It asks first and names each of those it finds; --yes skips the question. Then
it prints what left, by kind, and each thing that stays: a removal that failed,
a proveo binary outside the install root, a registered schedule tick with the
command that removes it, and each sandbox a run kept. ` + "`proveo rm <sandbox>`" + ` carries
a kept sandbox's work home; run it before uninstall, since nothing else does.

Does not remove Docker images, project .env files, host IDE homes, sbx, or the
state root (~/.local/state/proveo). A shell that ran proveo may keep its old
path: a new shell, ` + "`hash -r`" + ` (bash) or ` + "`rehash`" + ` (zsh) forgets it.
Use proveo clean --homes to reclaim proveo session caches under the install root
before uninstall if you want those gone separately — uninstall removes the whole
install root including homes that live there.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return doUninstall(yes) },
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the question (PROVEO_UNINSTALL_ASSUME_YES=1 does the same)")
	return cmd
}

func doUninstall(yes bool) error {
	return uninstall(yes || envflag.On(os.Getenv("PROVEO_UNINSTALL_ASSUME_YES")), agentio.IsStdinTTY(), os.Stdin)
}

// uninstall asks on a terminal unless yes, removes the PATH lines, the prompt kit and the install root, then
// prints what left as one block, what stays as ⚠ items, and one closing line.
func uninstall(yes, tty bool, in io.Reader) error {
	if !yes && !tty {
		return reported(sandbox.Item{Fail: true, Text: "proveo — not uninstalled",
			Detail: []string{"`proveo uninstall --yes` uninstalls it without asking"}}, errors.New("uninstall: no terminal to ask on"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return reported(sandbox.Item{Fail: true, Text: "proveo — not uninstalled", Detail: errLines(err)}, err)
	}
	root, rcs, kit := installRoot(), shellRCCandidates(home), promptKitOptions(false)
	found := findInstalled(root, rcs, kit)
	if q := found.question(); !yes && q != "" && !confirm(q, in) {
		ui.Warnf("uninstall — cancelled")
		return nil
	}
	gone := uninstallRecord{rootAt: resolved(root)}
	binDir := filepath.Join(root, "bin")
	for _, rc := range rcs {
		gone.strip(rc, binDir)
	}
	kit.Fact, kit.Warn = gone.kitFact, gone.warn
	_ = promptkit.Undo(kit)
	var rootErr error
	if found.root {
		if rootErr = removeInstallRoot(root); rootErr == nil {
			gone.root = root
		}
	}
	items := append(gone.stays, leftovers(root, gone.rootAt)...)
	facts := gone.facts()
	if rootErr != nil {
		sandbox.PrintBlock(func() {}, facts, append(items, rootItem(root, rootErr)))
		return backend.Reported{Err: rootErr}
	}
	verb := "uninstalled"
	switch {
	case len(facts) == 0 && found.any():
		verb = "nothing removed"
	case len(facts) == 0:
		verb = "nothing to remove"
	}
	closing := sandbox.Item{Ok: true, Text: "proveo — " + verb}
	if len(items) > 0 {
		closing = sandbox.Item{Warn: true, Text: fmt.Sprintf("proveo — %s; %d left", verb, len(items))}
	}
	sandbox.PrintBlock(func() {}, facts, append(items, closing))
	return nil
}

// installFound is what uninstall finds before it asks: the install root, PATH lines in an rc file, the prompt kit.
type installFound struct {
	at              string
	root, path, kit bool
}

func findInstalled(root string, rcs []string, kit promptkit.Options) installFound {
	f := installFound{at: root, root: isDir(root), kit: promptkit.Installed(kit)}
	binDir := filepath.Join(root, "bin")
	f.path = slices.ContainsFunc(rcs, func(rc string) bool {
		b, err := os.ReadFile(rc)
		_, holds := withoutPathLines(string(b), binDir)
		return err == nil && holds
	})
	return f
}

func (f installFound) any() bool { return f.root || f.path || f.kit }

// question names each part found, "remove ~/.proveo, its PATH lines and the prompt kit?"; "" when none is.
func (f installFound) question() string {
	var parts []string
	if f.root {
		parts = append(parts, ui.Tilde(f.at))
	}
	switch {
	case f.path && f.root:
		parts = append(parts, "its PATH lines")
	case f.path:
		parts = append(parts, "proveo's PATH lines")
	}
	if f.kit {
		parts = append(parts, "the prompt kit")
	}
	if len(parts) == 0 {
		return ""
	}
	last := len(parts) - 1
	if last == 0 {
		return "remove " + parts[0] + "?"
	}
	return "remove " + strings.Join(parts[:last], ", ") + " and " + parts[last] + "?"
}

// confirm asks q and reads the answer from in; only y or yes agrees.
func confirm(q string, in io.Reader) bool {
	ui.Askf("%s [y/N]", q)
	s, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes":
		return true
	}
	return false
}

// uninstallRecord is what left the host, by key, and each removal that failed as a ⚠ item.
type uninstallRecord struct {
	root, rootAt string
	path, prompt []string
	starship     string
	font         *ui.Fact
	stays        []sandbox.Item
}

func (u *uninstallRecord) strip(rc, binDir string) {
	switch changed, err := stripPathLines(rc, binDir); {
	case err != nil && changed:
		u.stays = append(u.stays, sandbox.Item{Warn: true, Text: ui.Tilde(rc) + " — PATH line not removed", Detail: errLines(err)})
	case err != nil:
		u.stays = append(u.stays, sandbox.Item{Warn: true, Text: ui.Tilde(rc) + " — not read", Detail: errLines(err)})
	case changed:
		u.path = append(u.path, rc)
	}
}

func (u *uninstallRecord) kitFact(key, value string, detail ...string) {
	switch key {
	case "starship":
		u.starship = value
	case "font":
		u.font = &ui.Fact{Role: ui.RoleError, Key: "font", Value: value, Detail: tildeIn(detail)}
	default:
		u.prompt = append(u.prompt, value)
	}
}

func (u *uninstallRecord) warn(text string, detail ...string) {
	u.stays = append(u.stays, sandbox.Item{Warn: true, Text: ui.TildeIn(text), Detail: tildeIn(detail)})
}

// facts is the record as one block: root · path · prompt · font, each valued with where it was. The starship
// binary stacks under prompt unless the root it sat in is the root fact.
func (u uninstallRecord) facts() []ui.Fact {
	var facts []ui.Fact
	stack := func(key string, values ...string) {
		for i, v := range values {
			f := ui.Fact{Role: ui.RoleError, Value: ui.Tilde(v)}
			if i == 0 {
				f.Key = key
			}
			facts = append(facts, f)
		}
	}
	if u.root != "" {
		stack("root", u.root)
	}
	stack("path", u.path...)
	prompt := u.prompt
	if u.starship != "" && (u.root == "" || !within(u.starship, u.root) && !within(resolved(filepath.Dir(u.starship)), u.rootAt)) {
		prompt = append(prompt, u.starship)
	}
	stack("prompt", prompt...)
	if u.font != nil {
		facts = append(facts, *u.font)
	}
	return facts
}

// leftovers is what proveo's own records say stays: a proveo binary outside the install root, a registered
// schedule tick with the command that removes it, and each sandbox a run kept.
func leftovers(root, rootAt string) []sandbox.Item {
	var items []sandbox.Item
	seen := map[string]bool{}
	if p, err := exec.LookPath("proveo"); err == nil && !within(resolved(p), rootAt) {
		seen[resolved(p)] = true
		items = append(items, sandbox.Item{Warn: true, Text: ui.Tilde(p) + " — still on PATH"})
	}
	if exe, err := executable(); err == nil && !seen[resolved(exe)] && !within(resolved(exe), rootAt) && exists(exe) {
		items = append(items, sandbox.Item{Warn: true, Text: ui.Tilde(exe) + " — not removed", Detail: []string{"outside " + ui.Tilde(root)}})
	}
	if slices.ContainsFunc(schedule.TickFiles(), exists) {
		items = append(items, sandbox.Item{Warn: true, Text: "schedule tick — still registered", Detail: []string{schedule.RemoveCommand(ui.Tilde)}})
	}
	for _, e := range results.List(results.Dir(run.StateDir())) {
		if e.Kind == results.Kept {
			items = append(items, sandbox.Item{Warn: true, Text: e.Key + " — kept, its output only inside"})
		}
	}
	return items
}

// rootItem is the closing × for an install root that stays.
func rootItem(root string, err error) sandbox.Item {
	if errors.Is(err, errUnsafeRoot) {
		return sandbox.Item{Fail: true, Text: ui.Tilde(root) + " — not removed", Detail: []string{"an unsafe install root"}}
	}
	if inner := errors.Unwrap(err); inner != nil {
		err = inner
	}
	return sandbox.Item{Fail: true, Text: ui.Tilde(root) + " — not fully removed", Detail: errLines(err)}
}

func tildeIn(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ui.TildeIn(l)
	}
	return out
}

// resolved is path with its symlinks resolved, or path when that fails.
func resolved(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}

// within reports whether path is dir or lies under it, ignoring case.
func within(path, dir string) bool {
	path, dir = strings.ToLower(filepath.Clean(path)), strings.ToLower(filepath.Clean(dir))
	return path == dir || strings.HasPrefix(path, dir+string(os.PathSeparator))
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func installRoot() string {
	if r := strings.TrimSpace(os.Getenv("PROVEO_INSTALL_ROOT")); r != "" {
		return r
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".proveo")
}

var errUnsafeRoot = errors.New("refusing to remove unsafe install root")

func removeInstallRoot(root string) error {
	home, _ := os.UserHomeDir()
	clean := filepath.Clean(root)
	if clean == "/" || clean == "." || clean == home || clean == filepath.Dir(home) {
		return fmt.Errorf("%w: %s", errUnsafeRoot, root)
	}
	if err := os.RemoveAll(clean); err != nil {
		return fmt.Errorf("remove %s: %w", clean, err)
	}
	return nil
}

func shellRCCandidates(home string) []string {
	return []string{
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".profile"),
		filepath.Join(home, ".config", "fish", "config.fish"),
	}
}

// stripPathLines takes install.sh's and proveo setup's PATH lines for binDir out of rc; changed says it held one.
func stripPathLines(rc, binDir string) (changed bool, err error) {
	b, err := os.ReadFile(rc)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	out, changed := withoutPathLines(string(b), binDir)
	if !changed {
		return false, nil
	}
	return true, writeRC(rc, out)
}

func withoutPathLines(content, binDir string) (string, bool) {
	content, install := withoutInstallPathBlock(content, binDir)
	content, setup := withoutSetupPathBlock(content, binDir)
	return content, install || setup
}

// withoutInstallPathBlock takes out each install.sh block whose PATH line names binDir or that holds no PATH line,
// and a bare install.sh PATH line for binDir; a block for another directory stays.
func withoutInstallPathBlock(content, binDir string) (string, bool) {
	posix := `export PATH="` + binDir + `:$PATH"`
	fish := `set -gx PATH "` + binDir + `" $PATH`
	lines := strings.Split(content, "\n")
	var out []string
	changed := false
	for i := 0; i < len(lines); i++ {
		if lines[i] == posix || lines[i] == fish {
			changed = true
			continue
		}
		if lines[i] != installPathMarkerStart {
			out = append(out, lines[i])
			continue
		}
		end := i + 1
		for end < len(lines) && lines[end] != installPathMarkerEnd {
			end++
		}
		body := lines[i+1 : end]
		other := slices.ContainsFunc(body, func(l string) bool {
			return strings.HasPrefix(l, "export PATH=") || strings.HasPrefix(l, "set -gx PATH ")
		}) && !slices.Contains(body, posix) && !slices.Contains(body, fish)
		if other {
			out = append(out, lines[i:min(end+1, len(lines))]...)
		} else {
			changed = true
		}
		i = end
	}
	return strings.Join(out, "\n"), changed
}

// withoutSetupPathBlock takes out each `proveo setup` block whose PATH line names binDir, and a marker with no
// PATH line under it; a block for another directory stays.
func withoutSetupPathBlock(content, binDir string) (string, bool) {
	lines := strings.Split(content, "\n")
	var out []string
	changed := false
	for i := 0; i < len(lines); i++ {
		if lines[i] != shell.Marker {
			out = append(out, lines[i])
			continue
		}
		next := ""
		if i+1 < len(lines) {
			next = lines[i+1]
		}
		switch {
		case setsPathTo(next, binDir):
			i++
		case strings.HasPrefix(next, "export PATH=") || strings.HasPrefix(next, "set -gx PATH ") || strings.HasPrefix(next, "setenv PATH "):
			out = append(out, lines[i])
			continue
		}
		changed = true
	}
	return strings.Join(out, "\n"), changed
}

// setsPathTo reports whether line is the PATH line `proveo setup` writes for binDir, in any shell's syntax.
func setsPathTo(line, binDir string) bool {
	return slices.ContainsFunc([]string{"zsh", "fish", "csh"}, func(name string) bool {
		return line == shell.Shell{Name: name}.PathLine(binDir)
	})
}

func writeRC(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
