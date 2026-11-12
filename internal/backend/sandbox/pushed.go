// SPEC: _spec/internal/sbx/clone-workspace.puml
package sandbox

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/ptyproxy"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

const fetchPushedWait = 15 * time.Second

var confirmFetchPushed = func(branches []string) bool {
	if !headedConfirm() {
		return false
	}
	ptyproxy.DiscardInput(os.Stdin)
	return askFetchPushed(branches, fetchPushedWait)
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

func gitReflog(ctx context.Context, dir string) (string, error) {
	cmd, finish := boundCommand(ctx, guestExecDeadline, "git", "-C", dir, "log", "-g", "--date=unix", "--all", "--format=%gd%x09%gs")
	out, err := cmd.Output()
	return string(out), finish(err)
}

func sandboxReflog(ctx context.Context, name, workdir string) (string, error) {
	cmd, finish := boundCommand(ctx, guestExecDeadline, sbx.Binary, "exec", "-w", "/", name, "--", "git", "-C", workdir, "log", "-g", "--date=unix", "--all", "--format=%gd%x09%gs")
	out, err := cmd.Output()
	return string(out), finish(err)
}

func offerPushedFetch(in Input) bool {
	return !in.Man.IsAssistant()
}

// reportPushedBranches names, into out, each branch the agent pushed during the run, and fetches
// them when the operator asks; the fetch commands otherwise go to the run log.
func reportPushedBranches(in Input, cfg sbx.RunConfig, since time.Time, out *Block) {
	var reflog string
	var err error
	switch {
	case in.Clone && sbx.Exists(cfg.Name):
		wd := agentWorkdir(in, cfg.Mounts)
		if wd == "" {
			return
		}
		reflog, err = sandboxReflog(in.ctx(), cfg.Name, wd)
	case !in.Clone && in.RepoRoot != "":
		reflog, err = gitReflog(in.ctx(), in.RepoRoot)
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
	fetched := map[string]bool{}
	defer func() { out.Fact(pushedFacts(branches, fetched)...) }()
	if !offerPushedFetch(in) || in.RepoRoot == "" || len(pushedFetchArgs(in.RepoRoot, branches)) == 0 {
		return
	}
	if !confirmFetchPushed(branches) {
		for _, line := range fetchPushedLines(in.RepoRoot, branches) {
			ui.Logf("%s", line)
		}
		return
	}
	for _, args := range pushedFetchArgs(in.RepoRoot, branches) {
		remote := args[4]
		cmd, finish := boundForeground(in.ctx(), guestExecDeadline, "git", args...)
		said, err := cmd.CombinedOutput()
		if err = finish(err); err != nil {
			for _, b := range args[5:] {
				out.Item(Item{Warn: true, Text: remote + "/" + b + " — not fetched", Detail: []string{failure(err, string(said))}})
			}
			continue
		}
		for _, b := range args[5:] {
			fetched[remote+"/"+b] = true
		}
	}
}

// pushedFacts is the pushed fact: one row per pushed branch, dim "fetched" under each one fetched.
func pushedFacts(branches []string, fetched map[string]bool) []ui.Fact {
	rows := make([]ui.Fact, 0, len(branches))
	for i, b := range branches {
		row := ui.Fact{Role: ui.RoleCloud, Value: b}
		if i == 0 {
			row.Key = "pushed"
		}
		if fetched[b] {
			row.Detail = []string{"fetched"}
		}
		rows = append(rows, row)
	}
	return rows
}
