// SPEC: _spec/internal/entrypoint/model-alias-bridges.puml, _spec/_paradigms/harness-paradigms.puml
package entrypoint

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strings"
)

var ConfigVars = []string{
	"ARCHITECT_MODEL",
	"EDITOR_MODEL",
	"SMALL_MODEL",
	"DARK_MODE",
	"CODE_THEME",
}

func EnsureRuntimeUser() {
	uid := fmt.Sprintf("%d", os.Getuid())
	gid := fmt.Sprintf("%d", os.Getgid())
	if _, err := user.LookupId(uid); err != nil {
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

func ApplyEnvBridges() {
	type bridge struct {
		from, to, fallback, def, transform string
	}
	bridges := []bridge{
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
}

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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
