// SPEC: _spec/internal/sbx/clone-workspace.puml
package sandbox

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

const fetchPushedWait = 15 * time.Second

var confirmFetchPushed = func(branches []string) bool {
	if !headedConfirm() {
		return false
	}
	screen, err := openScreen()
	if err != nil {
		return false
	}
	defer screen.Fini()
	return choiceui.ConfirmFetchPushed(screen, branches, fetchPushedWait)
}

var fetchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func splitPushed(name string) (string, string, bool) {
	remote, branch, ok := strings.Cut(name, "/")
	if !ok || !fetchName.MatchString(remote) || !safeBranch(branch) {
		return "", "", false
	}
	return remote, branch, true
}

func safeBranch(branch string) bool {
	if branch == "" || strings.Contains(branch, "..") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return false
	}
	for _, part := range strings.Split(branch, "/") {
		if !fetchName.MatchString(part) {
			return false
		}
	}
	return true
}

func pushedFetchArgs(repo string, branches []string) [][]string {
	grouped := map[string][]string{}
	var remotes []string
	for _, name := range branches {
		remote, branch, ok := splitPushed(name)
		if !ok {
			continue
		}
		if _, seen := grouped[remote]; !seen {
			remotes = append(remotes, remote)
		}
		dup := false
		for _, have := range grouped[remote] {
			if have == branch {
				dup = true
				break
			}
		}
		if !dup {
			grouped[remote] = append(grouped[remote], branch)
		}
	}
	sort.Strings(remotes)
	var cmds [][]string
	for _, remote := range remotes {
		names := append([]string(nil), grouped[remote]...)
		sort.Strings(names)
		args := append([]string{"-C", repo, "fetch", "--no-tags", remote}, names...)
		cmds = append(cmds, args)
	}
	return cmds
}

func fetchPushedLines(repo string, branches []string) []string {
	var lines []string
	for _, args := range pushedFetchArgs(repo, branches) {
		lines = append(lines, fmt.Sprintf("fetch: `git -C %s %s`", repo, strings.Join(args[2:], " ")))
	}
	return lines
}

var pushedRef = regexp.MustCompile(`^(.*)@\{(\d+)\}$`)

// pushedBranches reads `git log -g --date=unix` lines and returns each
// remote-tracking branch updated by push at or after since, once.
func pushedBranches(reflog string, since time.Time) []string {
	cutoff := since.Unix()
	seen := map[string]struct{}{}
	var out []string
	for _, line := range strings.Split(reflog, "\n") {
		ref, msg, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok || msg != "update by push" {
			continue
		}
		m := pushedRef.FindStringSubmatch(ref)
		if m == nil {
			continue
		}
		ts, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil || ts < cutoff {
			continue
		}
		name := m[1]
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func gitReflog(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "log", "-g", "--date=unix", "--all", "--format=%gd%x09%gs").Output()
	return string(out), err
}

func sandboxReflog(name, workdir string) (string, error) {
	out, err := exec.Command(sbx.Binary, "exec", "-w", "/", name, "--", "git", "-C", workdir, "log", "-g", "--date=unix", "--all", "--format=%gd%x09%gs").Output()
	return string(out), err
}

func reportPushedBranches(in Input, cfg sbx.RunConfig, since time.Time) {
	var reflog string
	var err error
	switch {
	case in.Clone && sbx.Exists(cfg.Name):
		wd := agentWorkdir(in, cfg.Mounts)
		if wd == "" {
			return
		}
		reflog, err = sandboxReflog(cfg.Name, wd)
	case !in.Clone && in.RepoRoot != "":
		reflog, err = gitReflog(in.RepoRoot)
	default:
		return
	}
	if err != nil {
		return
	}
	branches := pushedBranches(reflog, since)
	if len(branches) == 0 {
		return
	}
	ui.Section(ui.SectionResults)
	for _, branch := range branches {
		ui.Storef("pushed %s", branch)
	}
	if in.RepoRoot == "" || len(pushedFetchArgs(in.RepoRoot, branches)) == 0 {
		return
	}
	if !confirmFetchPushed(branches) {
		for _, line := range fetchPushedLines(in.RepoRoot, branches) {
			ui.Notef("%s", line)
		}
		return
	}
	for _, args := range pushedFetchArgs(in.RepoRoot, branches) {
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			ui.Warnf("fetch failed (%v): %s", err, strings.TrimSpace(string(out)))
			continue
		}
		ui.Storef("fetched %s", strings.Join(args[4:], " "))
	}
}
