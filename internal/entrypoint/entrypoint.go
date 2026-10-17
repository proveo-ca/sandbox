// SPEC: _spec/internal/entrypoint/model-alias-bridges.puml, _spec/_paradigms/harness-paradigms.puml
package entrypoint

import (
	"fmt"
	"os"
	"os/user"
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
