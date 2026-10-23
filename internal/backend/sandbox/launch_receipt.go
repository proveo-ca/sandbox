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
	"path/filepath"
	"sort"
	"strings"

	"github.com/proveo-ca/proveo/internal/choiceui"
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
}

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
		return []string{"no record of the settings it was created with"}
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

// retireIfStale replaces a kept sandbox whose stored settings differ from this run.
// A headed run asks first, for every harness. A non-interactive run still retires a
// stopped sandbox and leaves a running one in place.
func retireIfStale(in Input, cfg sbx.RunConfig, now launchReceipt, exists, running func(string) bool,
	retire func(Input, sbx.RunConfig, func(string, ...any)) error, report, warn func(string, ...any)) error {
	if !exists(cfg.Name) {
		return nil
	}
	changed := now.changes(readReceipt(cfg.Name))
	if len(changed) == 0 {
		return nil
	}
	live := running(cfg.Name)
	replace, asked := confirmStaleSandbox(cfg.Name, changed, live, cfg.Clone, opencodeTarget(in.Target))
	if asked && !replace {
		if live {
			warn("%s stays running. Settings this run changed (%s) will not apply. The next proveo run reattaches to it",
				cfg.Name, strings.Join(changed, ", "))
			return nil
		}
		return fmt.Errorf("sandbox %s kept. The next proveo run reattaches and asks again. sbx rm --force %s deletes the session; the next run creates a new sandbox with this same name, and this session does not persist. Settings that will not apply: %s",
			cfg.Name, cfg.Name, strings.Join(changed, ", "))
	}
	if !asked && live {
		warn("%s is running with settings this run changed (%s) — they will NOT apply; exit the session using it, "+
			"or `sbx rm --force %s`, then run again. That deletes the session. The next proveo run creates a new sandbox with this same name, and this session does not persist",
			cfg.Name, strings.Join(changed, ", "), cfg.Name)
		return nil
	}
	report("replacing %s (%s changed)", cfg.Name, strings.Join(changed, ", "))
	return retire(in, cfg, report)
}

var openTermScreen = openScreen

var confirmStaleSandbox = func(name string, changed []string, live, clone, history bool) (bool, bool) {
	if !headedConfirm() {
		return false, false
	}
	screen, err := openTermScreen()
	if err != nil {
		return false, true
	}
	defer screen.Fini()
	return choiceui.ConfirmSandboxUpgrade(screen, name, changed, live, clone, history), true
}

// sandboxClone reports whether sandbox name holds a clone: this run's mode when this run
// created it, else the sandbox's own answer; known is false when it gives none.
func sandboxClone(in Input, name string) (clones, known bool) {
	if in.CloneKnown {
		return in.Clone, true
	}
	cmd, finish := guestCommand(cloneProbeDeadline, sbx.Binary, sbx.CloneModeArgs(name)...)
	out, err := cmd.Output()
	if err = finish(err); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			ui.Warnf("clone: %s did not answer the clone-mode probe (%v)", name, err)
		}
		return false, false
	}
	switch strings.TrimSpace(string(out)) {
	case "clone":
		return true, true
	case "direct":
		return false, true
	}
	return false, false
}

// retireSandbox fetches the clone and agent state home, then removes the sandbox.
// It reports each step as that step starts.
func retireSandbox(in Input, cfg sbx.RunConfig, report func(string, ...any)) error {
	report("1/3 carrying its work home from %s", cfg.Name)
	in.Sid += "-retired"
	in.CloneKnown = false
	if err := PreserveClone(in, cfg); err != nil {
		return fmt.Errorf("%w — %s kept; `sbx rm --force %s` deletes it once the work is home", err, cfg.Name, cfg.Name)
	}
	if opencodeTarget(in.Target) {
		if err := carryOpenCodeHistory(in.HomeRoot, cfg.Name); err != nil {
			return fmt.Errorf("OpenCode history could not be carried out of %s (%v) — it is kept; `sbx rm --force %s` deletes it with its history", cfg.Name, err, cfg.Name)
		}
	}
	if out, err := SaveState(cfg.Name, cfg.Env, sbx.Exists(cfg.Name), SbxRun); err != nil {
		ui.Warnf("resume state not preserved (%v): %s", err, strings.TrimSpace(out))
	}
	report("2/3 sbx rm --force %s — deleting the session. This session does not persist", cfg.Name)
	if out, err := SbxRun(sbx.RemoveArgs(cfg.Name)...); err != nil && !sbx.NotFound(out) {
		return fmt.Errorf("remove stale %s: %w: %s", cfg.Name, err, strings.TrimSpace(out))
	}
	_ = os.Remove(receiptPath(cfg.Name))
	_ = os.Remove(legacyOwnerRecord(cfg.Name))
	report("3/3 this run creates a new sandbox named %s", cfg.Name)
	return nil
}
