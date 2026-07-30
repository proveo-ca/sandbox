package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/shell"
	"github.com/proveo-ca/proveo/internal/ui"
)

// install.sh PATH block markers (also stripped by apps/cli/public/cli/uninstall.sh).
const (
	installPathMarkerStart = "# Added by proveo install.sh"
	installPathMarkerEnd   = "# End added by proveo install.sh"
)

func uninstallCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the CDN-installed proveo binary and PATH markers",
		Long: `Remove the proveo install root (default ~/.proveo) and strip PATH markers
written by install.sh / proveo setup from common shell rc files.

Does not remove Docker images, project .env files, or host IDE homes.
Use proveo clean --homes to reclaim proveo session caches under the install root
before uninstall if you want those gone separately — uninstall removes the whole
install root including homes that live there.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return doUninstall(yes) },
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")
	return cmd
}

func doUninstall(yes bool) error {
	if !yes {
		switch strings.ToLower(os.Getenv("PROVEO_UNINSTALL_ASSUME_YES")) {
		case "1", "true", "yes":
			yes = true
		}
	}
	root := installRoot()
	if !yes && isStdinTTY() {
		fmt.Fprintf(os.Stderr, "This will remove proveo from %s. Continue? [y/N] ", root)
		s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "y", "yes":
		default:
			ui.Notef("Uninstall cancelled.")
			return nil
		}
	} else if !yes {
		return fmt.Errorf("refusing non-interactive uninstall without --yes (or set PROVEO_UNINSTALL_ASSUME_YES=1)")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	binDir := filepath.Join(root, "bin")
	for _, rc := range shellRCCandidates(home) {
		_ = stripInstallPathBlock(rc, binDir)
		_ = stripSetupPathBlock(rc)
	}

	if err := removeInstallRoot(root); err != nil {
		return err
	}

	if p, err := os.Executable(); err == nil {
		if _, err := os.Stat(p); err == nil && !strings.HasPrefix(p, root+string(os.PathSeparator)) && !strings.EqualFold(p, filepath.Join(root, "bin", "proveo")) {
			ui.Warnf("proveo is still resolvable at %s (not under the install root)", p)
			ui.Warnf("Open a new shell, or run 'hash -r' in bash / 'rehash' in zsh.")
			return nil
		}
	}
	ui.Okf("proveo uninstalled (removed %s)", root)
	return nil
}

func installRoot() string {
	if r := strings.TrimSpace(os.Getenv("PROVEO_INSTALL_ROOT")); r != "" {
		return r
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".proveo")
}

func removeInstallRoot(root string) error {
	home, _ := os.UserHomeDir()
	clean := filepath.Clean(root)
	if clean == "/" || clean == "." || clean == home || clean == filepath.Dir(home) {
		return fmt.Errorf("refusing to remove unsafe install root: %s", root)
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

// stripInstallPathBlock removes the install.sh-delimited PATH block and any
// lone PATH lines pointing at binDir.
func stripInstallPathBlock(rc, binDir string) error {
	b, err := os.ReadFile(rc)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines := strings.Split(string(b), "\n")
	var out []string
	skip := false
	changed := false
	posix := `export PATH="` + binDir + `:$PATH"`
	fish := `set -gx PATH "` + binDir + `" $PATH`
	for _, line := range lines {
		if line == installPathMarkerStart {
			skip = true
			changed = true
			continue
		}
		if line == installPathMarkerEnd {
			skip = false
			continue
		}
		if skip {
			changed = true
			continue
		}
		if line == posix || line == fish {
			changed = true
			continue
		}
		out = append(out, line)
	}
	if !changed {
		return nil
	}
	return writeRC(rc, strings.Join(out, "\n"))
}

// stripSetupPathBlock removes the `# added by proveo setup` marker + following PATH line.
func stripSetupPathBlock(rc string) error {
	b, err := os.ReadFile(rc)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines := strings.Split(string(b), "\n")
	var out []string
	changed := false
	for i := 0; i < len(lines); i++ {
		if lines[i] == shell.Marker {
			changed = true
			if i+1 < len(lines) && (strings.HasPrefix(lines[i+1], "export PATH=") ||
				strings.HasPrefix(lines[i+1], "set -gx PATH ") ||
				strings.HasPrefix(lines[i+1], "setenv PATH ")) {
				i++
			}
			continue
		}
		out = append(out, lines[i])
	}
	if !changed {
		return nil
	}
	return writeRC(rc, strings.Join(out, "\n"))
}

func writeRC(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
