// Package entrypoint implements the shared in-container harness prelude:
// runtime user, .env load, env bridges, git identity, smoke mode, and
// credential-broker sentinels.
//
// SPEC: _spec/internal/entrypoint/model-alias-bridges.puml, _spec/_paradigms/harness-paradigms.puml
package entrypoint

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultSentinel replaces real provider secrets in the agent process when the
// credential broker is active (firewall inject/strip at the MITM).
const DefaultSentinel = "proveo-brokered"

// ConfigVars are the NON-SECRET harness preferences (CODING_HARNESSES.md "Model
// aliases" / "UI aliases") forwarded into the agent BY VALUE. They cannot ride
// the project .env like the harness entrypoints assume: proxy/firewall mask
// every dotenv file under the mounted tree, and the input-output layout
// (claudecode) never mounts one at all — so without this the agent silently
// falls back to its baked-in default model instead of the one the user chose.
// Secrets never appear here; those stay on the broker (firewall) or a declared
// manifest env var.
var ConfigVars = []string{
	"ARCHITECT_MODEL",
	"EDITOR_MODEL",
	"SMALL_MODEL",
	"DARK_MODE",
	"CODE_THEME",
}

// EnsureRuntimeUser synthesizes a passwd entry for the current uid when missing
// and ensures HOME is writable (mirrors packages/lib/entrypoint-lib.sh).
func EnsureRuntimeUser() {
	uid := fmt.Sprintf("%d", os.Getuid())
	gid := fmt.Sprintf("%d", os.Getgid())
	if _, err := user.LookupId(uid); err != nil {
		// Best-effort passwd append when writable.
		if f, err := os.OpenFile("/etc/passwd", os.O_APPEND|os.O_WRONLY, 0); err == nil {
			_, _ = fmt.Fprintf(f, "agent:x:%s:%s:agent:%s:/bin/bash\n", uid, gid, firstNonEmpty(os.Getenv("HOME"), "/tmp"))
			_ = f.Close()
		}
	}
	home := os.Getenv("HOME")
	if home == "" || !writable(home) {
		_ = os.Setenv("HOME", "/tmp")
	}
}

func writable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	f, err := os.CreateTemp(path, ".proveo-w-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// FindEnvFile locates a .env near cwd / git root (same search order as bash).
func FindEnvFile(cwd string) string {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if exists(filepath.Join(cwd, ".env")) {
		return filepath.Join(cwd, ".env")
	}
	if out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output(); err == nil {
		root := strings.TrimSpace(string(out))
		if exists(filepath.Join(root, ".env")) {
			return filepath.Join(root, ".env")
		}
	}
	dir := cwd
	for {
		if exists(filepath.Join(dir, ".git")) && exists(filepath.Join(dir, ".env")) {
			return filepath.Join(dir, ".env")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// LoadEnvFile parses KEY=VALUE lines into the process environment (set -a style).
// Does not override keys already set in the environment (docker -e wins).
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 {
			if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
				v = v[1 : len(v)-1]
			}
		}
		if k == "" {
			continue
		}
		if _, set := os.LookupEnv(k); set {
			continue
		}
		_ = os.Setenv(k, v)
	}
	return sc.Err()
}

// ShouldSkipEnvLoad is true in proxy/firewall so secrets stay on the host/broker.
func ShouldSkipEnvLoad(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "proxy", "firewall":
		return true
	}
	return false
}

// BridgeGoogleKeys mirrors the GEMINI/GOOGLE key alias bridges from load_env.
func BridgeGoogleKeys() {
	if os.Getenv("GOOGLE_GENERATIVE_AI_API_KEY") == "" {
		if v := os.Getenv("GEMINI_API_KEY"); v != "" {
			_ = os.Setenv("GOOGLE_GENERATIVE_AI_API_KEY", v)
		} else if v := os.Getenv("GOOGLE_API_KEY"); v != "" {
			_ = os.Setenv("GOOGLE_GENERATIVE_AI_API_KEY", v)
		}
	}
	if v := os.Getenv("GOOGLE_GENERATIVE_AI_API_KEY"); v != "" {
		if os.Getenv("GEMINI_API_KEY") == "" {
			_ = os.Setenv("GEMINI_API_KEY", v)
		}
		if os.Getenv("GOOGLE_API_KEY") == "" {
			_ = os.Setenv("GOOGLE_API_KEY", v)
		}
	}
}

// ApplyBrokerSentinel rewrites listed env vars to the sentinel so the agent
// process never holds the real provider secret. keysCSV is comma-separated
// names; empty means no-op. Only rewrites when mode is firewall.
func ApplyBrokerSentinel(mode, keysCSV, sentinel string) []string {
	if strings.ToLower(strings.TrimSpace(mode)) != "firewall" {
		return nil
	}
	if strings.TrimSpace(keysCSV) == "" {
		return nil
	}
	if sentinel == "" {
		sentinel = DefaultSentinel
	}
	var rewritten []string
	for _, k := range strings.Split(keysCSV, ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if v := os.Getenv(k); v != "" && v != sentinel {
			_ = os.Setenv(k, sentinel)
			rewritten = append(rewritten, k)
		}
	}
	return rewritten
}

// BridgeGitIdentity sets GIT_CONFIG_* so git config --get resolves identity
// without writing files (when repo has no user.name/email).
func BridgeGitIdentity(dir string) {
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	name := firstNonEmpty(os.Getenv("GIT_AUTHOR_NAME"), os.Getenv("GIT_COMMITTER_NAME"))
	email := firstNonEmpty(os.Getenv("GIT_AUTHOR_EMAIL"), os.Getenv("GIT_COMMITTER_EMAIL"))
	idx := 0
	if n := os.Getenv("GIT_CONFIG_COUNT"); n != "" {
		_, _ = fmt.Sscanf(n, "%d", &idx)
	}
	if name != "" && exec.Command("git", "-C", dir, "config", "--get", "user.name").Run() != nil {
		_ = os.Setenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", idx), "user.name")
		_ = os.Setenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", idx), name)
		idx++
	}
	if email != "" && exec.Command("git", "-C", dir, "config", "--get", "user.email").Run() != nil {
		_ = os.Setenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", idx), "user.email")
		_ = os.Setenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", idx), email)
		idx++
	}
	if idx > 0 {
		_ = os.Setenv("GIT_CONFIG_COUNT", fmt.Sprintf("%d", idx))
	}
}

// NormalizeModel adds a provider prefix when missing (opencode-style).
func NormalizeModel(model string) string {
	if model == "" || strings.Contains(model, "/") {
		return model
	}
	ml := strings.ToLower(model)
	switch {
	case strings.HasPrefix(ml, "gpt-"), strings.HasPrefix(ml, "chatgpt-"), regexp.MustCompile(`^o[0-9]`).MatchString(ml):
		return "openai/" + model
	case strings.HasPrefix(ml, "claude-"):
		return "anthropic/" + model
	case strings.HasPrefix(ml, "grok-"):
		return "xai/" + model
	case strings.HasPrefix(ml, "gemini-"):
		return "google/" + model
	case strings.HasPrefix(ml, "deepseek-"):
		return "deepseek/" + model
	}
	return model
}

// ApplyEnvBridges applies the shared model/key bridge map.
func ApplyEnvBridges() {
	type bridge struct {
		from, to, fallback, def, transform string
	}
	bridges := []bridge{
		{from: "ARCHITECT_MODEL", to: "OPENCODE_MODEL", fallback: "EDITOR_MODEL", def: "anthropic/claude-sonnet-4-5", transform: "normalize"},
		{from: "EDITOR_MODEL", to: "OPENCODE_BUILD_MODEL", def: "$OPENCODE_MODEL", transform: "normalize"},
		{from: "EDITOR_MODEL", to: "OPENCODE_SMALL_MODEL", fallback: "SMALL_MODEL", def: "anthropic/claude-haiku-4-5", transform: "normalize"},
		{from: "OPENCODE_SMALL_MODEL", to: "SMALL_MODEL", transform: "normalize"},
		{from: "GEMINI_API_KEY", to: "GOOGLE_GENERATIVE_AI_API_KEY"},
		{from: "GOOGLE_API_KEY", to: "GOOGLE_GENERATIVE_AI_API_KEY"},
	}
	for _, b := range bridges {
		if os.Getenv(b.to) != "" {
			continue
		}
		src := os.Getenv(b.from)
		if src == "" && b.fallback != "" {
			src = os.Getenv(b.fallback)
		}
		if src == "" && b.def != "" {
			if strings.HasPrefix(b.def, "$") {
				src = os.Getenv(strings.TrimPrefix(b.def, "$"))
			} else {
				src = b.def
			}
		}
		if src == "" {
			continue
		}
		if b.transform == "normalize" {
			src = NormalizeModel(src)
		}
		_ = os.Setenv(b.to, src)
	}
	if os.Getenv("OPENCODE_SMALL_MODEL") == "" && os.Getenv("SMALL_MODEL") != "" {
		_ = os.Setenv("OPENCODE_SMALL_MODEL", os.Getenv("SMALL_MODEL"))
	}
}

// SmokeReady prints the smoke sentinel and returns true when PROVEO_SMOKE_TEST=1.
func SmokeReady(target string) bool {
	if os.Getenv("PROVEO_SMOKE_TEST") != "1" {
		return false
	}
	name := os.Getenv("PROVEO_SMOKE_TARGET")
	if name == "" {
		name = target
	}
	fmt.Printf("✅ PROVEO_SMOKE_READY %s\n", name)
	return true
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
