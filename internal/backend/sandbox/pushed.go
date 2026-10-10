// SPEC: _spec/internal/sbx/clone-workspace.puml
package sandbox

import (
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

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
}
