// SPEC: _spec/internal/sbx/kit-lifecycle.puml
package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// launchReceipt is what a sandbox was created with, per-run values normalised away.
type launchReceipt struct {
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

func receiptOf(cfg sbx.RunConfig, kitYAML []byte, sid, imageID string) launchReceipt {
	norm := func(s string) string {
		if sid == "" {
			return s
		}
		return strings.ReplaceAll(s, sid, "<sid>")
	}
	r := launchReceipt{
		Image: cfg.Image, ImageID: imageID, Kit: digest(norm(string(kitYAML))),
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

// retireIfStale re-creates a stopped kept sandbox whose stored settings differ from this run's, and warns for a running one.
func retireIfStale(in Input, cfg sbx.RunConfig, now launchReceipt, exists, running func(string) bool,
	retire func(Input, sbx.RunConfig) error, report, warn func(string, ...any)) error {
	if !exists(cfg.Name) {
		return nil
	}
	changed := now.changes(readReceipt(cfg.Name))
	if len(changed) == 0 {
		return nil
	}
	if running(cfg.Name) {
		warn("%s is running with settings this run changed (%s) — they will NOT apply; exit the session using it, "+
			"or `sbx rm --force %s`, then run again", cfg.Name, strings.Join(changed, ", "), cfg.Name)
		return nil
	}
	report("%s was created with different settings (%s) — carrying its work home and re-creating it so this run's choices apply",
		cfg.Name, strings.Join(changed, ", "))
	return retire(in, cfg)
}

// retireSandbox fetches the clone and agent state home, then removes the sandbox.
func retireSandbox(in Input, cfg sbx.RunConfig) error {
	PreserveClone(in, cfg)
	if out, err := SaveState(cfg.Name, cfg.Env, sbx.Exists(cfg.Name), SbxRun); err != nil {
		ui.Warnf("resume state not preserved (%v): %s", err, strings.TrimSpace(out))
	}
	if out, err := SbxRun(sbx.RemoveArgs(cfg.Name)...); err != nil && !sbx.NotFound(out) {
		return fmt.Errorf("remove stale %s: %w: %s", cfg.Name, err, strings.TrimSpace(out))
	}
	_ = os.Remove(receiptPath(cfg.Name))
	return nil
}
