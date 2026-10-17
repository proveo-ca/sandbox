// SPEC: _spec/internal/sbx/ide-attach.puml
package sandbox

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/ui"
)

// HomeAccess is the narrow host-state view an sbx run receives. The whole
// proveo home is deliberately absent: an attached editor must not inherit
// every prior run log merely because the agent needs its own config and tools.
type HomeAccess struct {
	Root         string
	FilesRoot    string
	Mounts       []runner.Mount
	SkippedLinks []string // "<link> → <target>" for links whose target LinkedDirsFile does not list
	files        []string
}

// LinkedDirsFile, at the proveo home root, lists the absolute targets a home dir's links may reach.
const LinkedDirsFile = "linked-dirs"

func noteSkippedLinks(a HomeAccess) {
	for _, l := range a.SkippedLinks {
		ui.Notef("home link %s not followed — list its target in %s to mount it read-only", l, filepath.Join(a.Root, LinkedDirsFile))
	}
}

func PrepareHomeAccess(root, runDir string, h manifest.Home) (HomeAccess, error) {
	a := HomeAccess{Root: root}
	if strings.TrimSpace(root) == "" || !h.Active() {
		return a, nil
	}
	seen := map[string]bool{}
	addDir := func(path string) error {
		path = filepath.Clean(path)
		if seen[path] {
			return nil
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		seen[path] = true
		a.Mounts = append(a.Mounts, runner.Mount{Host: path})
		return nil
	}
	for _, m := range h.Mounts {
		rel := strings.Trim(strings.TrimSpace(m.Host), "/")
		if rel == "" {
			continue
		}
		if err := addDir(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			return HomeAccess{}, fmt.Errorf("sandbox home access: %w", err)
		}
	}
	declared := len(a.Mounts)
	if err := addDir(filepath.Join(root, "toolchains")); err != nil {
		return HomeAccess{}, fmt.Errorf("sandbox home access: %w", err)
	}
	listed := listedLinkTargets(root)
	for _, m := range a.Mounts[:declared] {
		links, skipped := linkedDirs(root, m.Host, listed)
		a.SkippedLinks = append(a.SkippedLinks, skipped...)
		for _, l := range links {
			if !seen[l.Host] {
				seen[l.Host] = true
				a.Mounts = append(a.Mounts, l)
			}
		}
	}
	if len(h.Files) == 0 {
		return a, nil
	}
	a.FilesRoot = filepath.Join(runDir, "sbx", "home-files")
	if err := addDir(a.FilesRoot); err != nil {
		return HomeAccess{}, fmt.Errorf("sandbox home files: %w", err)
	}
	for _, name := range h.Files {
		name = strings.TrimSpace(name)
		if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
			continue
		}
		a.files = append(a.files, name)
		if err := copyNewerFile(filepath.Join(root, name), filepath.Join(a.FilesRoot, name)); err != nil {
			return HomeAccess{}, fmt.Errorf("sandbox home file %s: %w", name, err)
		}
	}
	return a, nil
}

// listedLinkTargets is the resolved set of absolute targets root's LinkedDirsFile names.
func listedLinkTargets(root string) map[string]bool {
	b, err := os.ReadFile(filepath.Join(root, LinkedDirsFile))
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !filepath.IsAbs(line) {
			continue
		}
		set[realPath(filepath.Clean(line))] = true
	}
	return set
}

// linkedDirs mounts read-only the targets of dir's first-level symlinks that
// leave the proveo home and appear in listed, and returns the links it skipped.
// SPEC: _spec/internal/sbx/ide-attach.puml (LINKED DIRS FOLLOW, READ-ONLY)
func linkedDirs(root, dir string, listed map[string]bool) (out []runner.Mount, skipped []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	home := realPath(root)
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		target, err := filepath.EvalSymlinks(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
			continue
		}
		if within(target, home) || within(home, target) || len(strings.Split(strings.Trim(filepath.ToSlash(target), "/"), "/")) < 2 {
			continue
		}
		if !listed[target] {
			skipped = append(skipped, filepath.Join(dir, e.Name())+" → "+target)
			continue
		}
		out = append(out, runner.Mount{Host: target, ReadOnly: true})
	}
	return out, skipped
}

func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}

// Commit copies home-root config files back after the in-VM config sync. The
// newest side wins, so a host edit made during the run is never overwritten by
// the copy staged when the run started.
func (a HomeAccess) Commit() error {
	if a.FilesRoot == "" {
		return nil
	}
	for _, name := range a.files {
		if err := copyNewerFile(filepath.Join(a.FilesRoot, name), filepath.Join(a.Root, name)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func (a HomeAccess) Cleanup() {
	if a.FilesRoot != "" {
		_ = os.RemoveAll(a.FilesRoot)
	}
}

func copyNewerFile(src, dst string) error {
	s, err := os.Stat(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if d, err := os.Stat(dst); err == nil && !s.ModTime().After(d.ModTime()) {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".proveo-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
		}
	}()
	if err := tmp.Chmod(s.Mode().Perm()); err != nil {
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chtimes(tmpName, time.Now(), s.ModTime()); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	ok = true
	return nil
}
