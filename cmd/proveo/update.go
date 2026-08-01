// SPEC: _spec/internal/cdn/distribution-update.puml
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/cdn"
	"github.com/proveo-ca/proveo/internal/ui"
)

func updateCmd() *cobra.Command {
	var force, checkOnly bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Download and install the latest proveo from the CDN release channel",
		Long: `Fetch latest.json from the proveo CDN, verify the platform binary checksum,
and atomically replace this executable when a newer version is available.

Skips self-update for local/dev builds unless --force is set.
Override the channel with PROVEO_ASSET_BASE_URL (default https://proveo.ca/cli).`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return doUpdate(force, checkOnly)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "update even from a dev/local build, or reinstall the same version")
	cmd.Flags().BoolVar(&checkOnly, "check", false, "print whether an update is available and exit")
	return cmd
}

func doUpdate(force, checkOnly bool) error {
	base := cdn.BaseURL()
	man, err := cdn.FetchManifest(nil, base)
	if err != nil {
		return err
	}
	asset := cdn.CurrentAssetName()
	sum, err := man.ChecksumFor(asset)
	if err != nil {
		return err
	}

	local := strings.TrimPrefix(version, "v")
	remote := man.Version
	need := force || cdn.Newer(remote, local)
	if checkOnly {
		if need && remote != local {
			ui.Notef("update available: %s → %s (%s)", local, remote, base)
			return nil
		}
		ui.Okf("up to date (%s)", local)
		return nil
	}
	if !need {
		ui.Okf("already on latest (%s)", local)
		return nil
	}
	if cdn.IsDevVersion(local) && !force {
		return fmt.Errorf("current build is %q — refuse to replace with CDN %s without --force", local, remote)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	tmpDir, err := os.MkdirTemp("", "proveo-update-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	tmp := filepath.Join(tmpDir, asset)
	ui.Iconf("⬇️", "downloading proveo %s (%s)…", remote, asset)
	if err := cdn.DownloadAsset(nil, man.BaseURL, asset, tmp, sum); err != nil {
		return err
	}

	if err := replaceExecutable(exe, tmp); err != nil {
		return err
	}
	if root := installRoot(); strings.HasPrefix(exe, filepath.Join(root, "bin")) {
		if err := refreshUninstallScript(root, man.BaseURL); err != nil {
			ui.Warnf("could not refresh uninstall.sh: %v", err)
		}
	}
	ui.Okf("updated proveo %s → %s", local, remote)
	return nil
}

// replaceExecutable atomically replaces dst with src (rename-first for Windows locks).
func replaceExecutable(dst, src string) error {
	old := dst + ".old"
	_ = os.Remove(old)
	renamed := false
	if err := os.Rename(dst, old); err == nil {
		renamed = true
	} else if !os.IsNotExist(err) {
		// Cross-device or busy: copy to sibling then rename.
		newPath := dst + ".new"
		if err := copyFileMode(src, newPath, 0o755); err != nil {
			return err
		}
		if err := os.Rename(newPath, dst); err != nil {
			_ = os.Remove(newPath)
			return err
		}
		_ = os.Remove(old)
		return nil
	}
	if err := os.Rename(src, dst); err != nil {
		if renamed {
			_ = os.Rename(old, dst)
		}
		// Last resort: copy over dst
		if err2 := copyFileMode(src, dst, 0o755); err2 != nil {
			return fmt.Errorf("install new binary: %w (rollback: %v)", err2, err)
		}
	}
	_ = os.Chmod(dst, 0o755)
	_ = os.Remove(old)
	return nil
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, mode)
}

func refreshUninstallScript(root, baseURL string) error {
	client := &http.Client{Timeout: 60 * time.Second}
	u := strings.TrimRight(baseURL, "/") + "/uninstall.sh"
	resp, err := client.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	dest := filepath.Join(root, "uninstall.sh")
	tmp := dest + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}
