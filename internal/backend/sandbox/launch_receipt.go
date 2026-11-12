// SPEC: _spec/internal/sbx/kit-lifecycle.puml
package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// launchReceipt is what a sandbox was created with, per-run values normalised away.
type launchReceipt struct {
	Agent   string            `json:"agent"`
	Image   string            `json:"image"`
	ImageID string            `json:"imageId,omitempty"`
	Kit     string            `json:"kit"`
	Clone   bool              `json:"clone"`
	Mounts  []string          `json:"mounts"`
	Env     map[string]string `json:"env"`
	CPUs    int               `json:"cpus,omitempty"`   // a record for the re-attach facts; changes ignores it
	Memory  string            `json:"memory,omitempty"` // a record for the re-attach facts; changes ignores it
	KitDir  string            `json:"kitDir,omitempty"` // a record for the re-attach facts; changes ignores it
	Publish []string          `json:"publish"`          // a record for the re-attach facts; changes ignores it; nil is unknown
	// OutputDir is the host output dir a clone's output lifts into; `proveo rm` lifts it. changes ignores it.
	OutputDir string `json:"outputDir,omitempty"`
}

// noReceipt is the change a sandbox with no receipt reports.
const noReceipt = "no record of the settings it was created with"

var receiptDir = func() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "proveo", "sbx-sandboxes")
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

func receiptOf(cfg sbx.RunConfig, image string, kitYAML []byte, sid, imageID string) launchReceipt {
	norm := func(s string) string {
		if sid == "" {
			return s
		}
		return strings.ReplaceAll(s, sid, "<sid>")
	}
	r := launchReceipt{
		Agent: cfg.Agent,
		Image: image, ImageID: imageID, Kit: digest(norm(string(kitYAML))),
		Clone: cfg.Clone, Env: map[string]string{},
		CPUs: cfg.CPUs, Memory: cfg.Memory,
		KitDir: cfg.KitDir, Publish: append([]string{}, cfg.Publish...),
	}
	for _, m := range cfg.Mounts {
		r.Mounts = append(r.Mounts, fmt.Sprintf("%s:%s:%v", norm(m.Host), m.Container, m.ReadOnly))
	}
	sort.Strings(r.Mounts)
	for _, kv := range cfg.Env {
		k, v, _ := strings.Cut(kv, "=")
		r.Env[k] = digest(norm(v))
	}
	return r
}

// changes names what differs from the receipt a sandbox was created with; nil when nothing does.
func (r launchReceipt) changes(was *launchReceipt) []string {
	if was == nil {
		return []string{noReceipt}
	}
	var out []string
	if r.Agent != was.Agent {
		out = append(out, "agent")
	}
	if r.Image != was.Image || (r.ImageID != "" && was.ImageID != "" && r.ImageID != was.ImageID) {
		out = append(out, "image")
	}
	if r.Kit != was.Kit {
		out = append(out, "kit")
	}
	if r.Clone != was.Clone {
		out = append(out, "clone")
	}
	if strings.Join(r.Mounts, "\n") != strings.Join(was.Mounts, "\n") {
		out = append(out, "mounts")
	}
	keys := map[string]bool{}
	for k := range r.Env {
		keys[k] = true
	}
	for k := range was.Env {
		keys[k] = true
	}
	var env []string
	for k := range keys {
		if r.Env[k] != was.Env[k] {
			env = append(env, k)
		}
	}
	sort.Strings(env)
	return append(out, env...)
}

func receiptPath(name string) string {
	dir := receiptDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name+".json")
}

func readReceipt(name string) *launchReceipt {
	p := receiptPath(name)
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var r launchReceipt
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

func writeReceipt(name string, r launchReceipt) {
	p := receiptPath(name)
	if p == "" || os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	if b, err := json.Marshal(r); err == nil {
		_ = os.WriteFile(p, b, 0o600)
	}
}

// retireIfStale replaces a kept sandbox whose stored settings differ from this run (changed).
// A headed run asks first, for every harness. A non-interactive run still retires a
// stopped sandbox and leaves a running one in place. kept is changed when the sandbox stays.
func retireIfStale(in Input, cfg sbx.RunConfig, changed []string, running func(string) bool,
	retire func(Input, sbx.RunConfig, func(string, ...any)) error, report func(string, ...any),
	warn func(head string, detail ...string)) (kept []string, err error) {
	if len(changed) == 0 {
		return nil, nil
	}
	live := running(cfg.Name)
	replace, asked := confirmStaleSandbox(cfg.Name, changed, live, cfg.Clone, opencodeTarget(in.Target))
	notApplied := "not applied: " + strings.Join(changed, ", ")
	if asked && !replace {
		if live {
			warn(cfg.Name+" — kept running without this run's changes", notApplied, "the next proveo run reattaches to it")
			return changed, nil
		}
		ui.Logf("`sbx rm --force %s` deletes the session; the next run creates a new sandbox with this same name, and this session does not persist. Settings that will not apply: %s",
			cfg.Name, strings.Join(changed, ", "))
		return nil, fmt.Errorf("sandbox %s kept; the next proveo run reattaches and asks again", cfg.Name)
	}
	if !asked && live {
		warn(cfg.Name+" — kept running without this run's changes", notApplied,
			fmt.Sprintf("`sbx rm --force %s` deletes it and its session; the next run builds it fresh", cfg.Name))
		return changed, nil
	}
	report("replacing %s — %s changed", cfg.Name, strings.Join(changed, ", "))
	return nil, retire(in, cfg, report)
}

// staleChanges names what differs from the receipt of the sandbox name; nil when no such sandbox exists or nothing differs.
func staleChanges(name string, now launchReceipt, exists bool) []string {
	if !exists {
		return nil
	}
	return now.changes(readReceipt(name))
}

// warnItem prints a ⚠ line with its dim detail lines.
func warnItem(head string, detail ...string) { Item{Warn: true, Text: head, Detail: detail}.print() }

var confirmStaleSandbox = func(name string, changed []string, live, clone, history bool) (bool, bool) {
	if !headedConfirm() {
		return false, false
	}
	yes, _ := askSandboxUpgrade(name, changed, live, clone, history)
	return yes, true
}

// errNoCloneAnswer marks a sandbox that gave no answer to the clone-mode probe.
var errNoCloneAnswer = errors.New("no answer to the clone-mode probe")

// sandboxClone reports whether sandbox name holds a clone: this run's mode when this run
// created it, else the sandbox's own answer; err is set when it gives none.
func sandboxClone(in Input, name string) (bool, error) {
	if in.CloneKnown {
		return in.Clone, nil
	}
	cmd, finish := boundCommand(in.ctx(), cloneProbeDeadline, sbx.Binary, sbx.CloneModeArgs(name)...)
	out, err := cmd.Output()
	if err = finish(err); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			ui.Logf("clone: %s did not answer the clone-mode probe (%v)", name, err)
			return false, fmt.Errorf("%w: %v", errNoCloneAnswer, err)
		}
		return false, err
	}
	switch mode := strings.TrimSpace(string(out)); mode {
	case "clone":
		return true, nil
	case "direct":
		return false, nil
	default:
		return false, fmt.Errorf("unreadable clone mode %q", mode)
	}
}

// retireSandbox fetches the clone and agent state home, then removes the sandbox.
// It reports each step as that step starts.
func retireSandbox(in Input, cfg sbx.RunConfig, report func(string, ...any)) error {
	report("carrying its work home from %s", cfg.Name)
	in.Sid += "-retired"
	in.CloneKnown = false
	carried := NewBlock(SbxHeading)
	err := PreserveClone(in, cfg, carried)
	carried.Print()
	if err != nil {
		if !errors.Is(err, errNoCloneAnswer) {
			return fmt.Errorf("%w — %s kept; %s", err, cfg.Name, keptFix(cfg.Name, sbx.Running(cfg.Name)))
		}
		refs := mirroredRefs(in.RepoRoot, cfg.Name)
		if !confirmUnansweredRemoval(cfg.Name, refs, opencodeTarget(in.Target)) {
			return fmt.Errorf("%w — %s kept; %s. `sbx rm --force %s` deletes it and any work past that",
				err, cfg.Name, mirrorNote(cfg.Name, refs), cfg.Name)
		}
		return removeRetired(cfg, report)
	}
	if opencodeTarget(in.Target) {
		if err := carryOpenCodeHistory(in.HomeRoot, cfg.Name); err != nil {
			return fmt.Errorf("OpenCode history could not be carried out of %s (%v) — it is kept; `sbx rm --force %s` deletes it with its history", cfg.Name, err, cfg.Name)
		}
	}
	if out, err := SaveState(cfg.Name, cfg.Env, sbx.Exists(cfg.Name), guestRun); err != nil {
		carried.Item(Item{Warn: true, Text: "resume state — not saved", Detail: []string{failure(err, out)}})
	}
	return removeRetired(cfg, report)
}

// removeRetired removes a retired sandbox and its records, bounded by guestExecDeadline.
func removeRetired(cfg sbx.RunConfig, report func(string, ...any)) error {
	report("sbx rm --force %s — deleting it and its session", cfg.Name)
	if out, err := guestRun(sbx.RemoveArgs(cfg.Name)...); err != nil && !sbx.NotFound(out) {
		return fmt.Errorf("remove stale %s: %w: %s — check `sbx ls`, then `sbx rm --force %s` once sbx answers", cfg.Name, err, strings.TrimSpace(out), cfg.Name)
	}
	_ = os.Remove(receiptPath(cfg.Name))
	_ = os.Remove(legacyOwnerRecord(cfg.Name))
	report("%s — this run creates it anew", cfg.Name)
	return nil
}

// mirroredRefs lists the refs sbx mirrored from sandbox name into the host repository.
func mirroredRefs(repo, name string) []string {
	if repo == "" {
		return nil
	}
	out, err := exec.Command("git", "-C", repo, "for-each-ref", "--format=%(refname)", sbx.MirrorRefs(name)+"/").Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

func mirrorNote(name string, refs []string) string {
	if len(refs) == 0 {
		return "nothing of it is mirrored on the host"
	}
	return fmt.Sprintf("its branches are mirrored under %s/ (%d refs)", sbx.MirrorRefs(name), len(refs))
}

var confirmUnansweredRemoval = func(name string, refs []string, history bool) bool {
	if !headedConfirm() {
		return false
	}
	yes, _ := askUnansweredRemoval(name, sbx.MirrorRefs(name), refs, history)
	return yes
}
