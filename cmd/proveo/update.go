// SPEC: _spec/internal/cdn/distribution-update.puml
package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
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
		Args: noArgs,
		RunE: func(*cobra.Command, []string) error {
			return doUpdate(force, checkOnly)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "update even from a dev/local build, or reinstall the same version")
	cmd.Flags().BoolVar(&checkOnly, "check", false, "print whether an update is available and exit; `proveo update` installs it")
	return cmd
}

func doUpdate(force, checkOnly bool) error {
	exe, err := executable()
	if err != nil {
		return reported(sandbox.Item{Fail: true, Text: "proveo — not updated", Detail: errLines(err)}, err)
	}
	return updateAt(resolved(exe), force, checkOnly)
}

// updateAt reads the release channel and, unless checkOnly, replaces the binary at exe with its release,
// printing one closing line; every failure is one × item, Reported.
func updateAt(exe string, force, checkOnly bool) error {
	base := cdn.BaseURL()
	done := ui.Running(func(ran time.Duration) string { return "latest.json — reading · " + ran.Round(time.Second).String() }, "")
	man, err := cdn.FetchManifest(nil, base)
	done()
	if err != nil {
		return reported(manifestItem(err), err)
	}
	asset := cdn.CurrentAssetName()
	sum, err := man.ChecksumFor(asset)
	if err != nil {
		return reported(sandbox.Item{Fail: true, Text: "latest.json — no checksum for " + asset}, err)
	}

	local := strings.TrimPrefix(version, "v")
	remote := man.Version
	need := force || cdn.Newer(remote, local)
	if checkOnly && need && remote != local {
		ui.Cloudf("proveo %s → %s — available", local, remote)
		ui.Detailf("%s", base)
		return nil
	}
	if checkOnly || !need {
		ui.Okf("proveo %s — up to date", local)
		return nil
	}
	if cdn.IsDevVersion(local) && !force {
		return reported(sandbox.Item{Fail: true, Text: "proveo " + local + " — a local build, not replaced",
			Detail: []string{"`proveo update --force` replaces it with " + remote}}, fmt.Errorf("update: %s is a local build", local))
	}

	tmpDir, err := os.MkdirTemp("", "proveo-update-*")
	if err != nil {
		return reported(sandbox.Item{Fail: true, Text: "proveo — not updated", Detail: errLines(err)}, err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	tmp := filepath.Join(tmpDir, asset)
	done = ui.Running(func(ran time.Duration) string {
		return "proveo " + remote + " — downloading · " + ran.Round(time.Second).String()
	}, "proveo "+remote+" — downloading")
	err = cdn.DownloadAsset(nil, man.BaseURL, asset, tmp, sum)
	done()
	if err != nil {
		return reported(downloadItem(remote, err), err)
	}
	if err := replaceExecutable(exe, tmp); err != nil {
		return reported(sandbox.Item{Fail: true, Text: ui.Tilde(exe) + " — not replaced", Detail: errLines(err)}, err)
	}

	var items []sandbox.Item
	if root := installRoot(); strings.HasPrefix(exe, filepath.Join(root, "bin")) {
		done = ui.Running(func(ran time.Duration) string {
			return "uninstall.sh — downloading · " + ran.Round(time.Second).String()
		}, "")
		err := refreshUninstallScript(root, man.BaseURL)
		done()
		if err != nil {
			items = append(items, sandbox.Item{Warn: true, Text: ui.Tilde(filepath.Join(root, "uninstall.sh")) + " — not refreshed", Detail: errLines(err)})
		}
	}
	verdict := "proveo " + local + " → " + remote + " — updated"
	if local == remote {
		verdict = "proveo " + remote + " — reinstalled"
	}
	sandbox.PrintBlock(func() {}, nil, append(items, sandbox.Item{Ok: true, Text: verdict, Detail: []string{ui.Tilde(exe)}}))
	return nil
}

// manifestItem is the × for a latest.json that gave no release: one that never arrived, or one that held none.
func manifestItem(err error) sandbox.Item {
	var me cdn.ManifestError
	if !errors.As(err, &me) {
		return sandbox.Item{Fail: true, Text: "latest.json — not read", Detail: errLines(err)}
	}
	if me.Fetch {
		return sandbox.Item{Fail: true, Text: "latest.json — not read", Detail: errLines(me.Err)}
	}
	return sandbox.Item{Fail: true, Text: "latest.json — not valid", Detail: errLines(me.Err)}
}

// downloadItem is the × for a release that did not arrive, or whose sha256 is not latest.json's.
func downloadItem(release string, err error) sandbox.Item {
	var mismatch cdn.ChecksumMismatch
	if errors.As(err, &mismatch) {
		return sandbox.Item{Fail: true, Text: mismatch.Asset + " " + release + " — checksum mismatch",
			Detail: []string{"expected " + mismatch.Want, "got      " + mismatch.Got}}
	}
	var de cdn.DownloadError
	if errors.As(err, &de) {
		err = de.Err
	}
	return sandbox.Item{Fail: true, Text: "proveo " + release + " — not downloaded", Detail: errLines(err)}
}

func replaceExecutable(dst, src string) error {
	old := dst + ".old"
	_ = os.Remove(old)
	renamed := false
	if err := os.Rename(dst, old); err == nil {
		renamed = true
	} else if !os.IsNotExist(err) {
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
