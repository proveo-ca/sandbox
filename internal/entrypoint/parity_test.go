package entrypoint

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var bashBridge = regexp.MustCompile(`(?m)^\s*_apply_env_bridge\s+(\S+)\s+(\S+)\s+(\S+|"")\s+('[^']*'|"[^"]*")\s+("[^"]*"|\S*)\s*$`)

var goBridge = regexp.MustCompile(`\{from: "([^"]*)", to: "([^"]*)"(?:, fallback: "([^"]*)")?(?:, def: "([^"]*)")?(?:, transform: "([^"]*)")?\}`)

func TestGoBashBridgeParity(t *testing.T) {
	t.Parallel()

	shPath := filepath.Join("..", "..", "packages", "lib", "entrypoint-lib.sh")
	sh, err := os.ReadFile(shPath)
	if err != nil {
		t.Fatalf("read %s: %v", shPath, err)
	}
	goPath := filepath.Join("entrypoint.go")
	gosrc, err := os.ReadFile(goPath)
	if err != nil {
		t.Fatalf("read %s: %v", goPath, err)
	}

	unquote := func(s string) string { return strings.Trim(s, `"'`) }

	bash := map[string]string{}
	for _, m := range bashBridge.FindAllStringSubmatch(string(sh), -1) {
		from, to := m[1], m[2]
		bash[from+"→"+to] = unquote(m[3]) + "|" + unquote(m[4]) + "|" + unquote(m[5])
	}
	if len(bash) == 0 {
		t.Fatal("parsed zero bridges from entrypoint-lib.sh — the parser has drifted from the script")
	}

	gomap := map[string]string{}
	for _, m := range goBridge.FindAllStringSubmatch(string(gosrc), -1) {
		gomap[m[1]+"→"+m[2]] = m[3] + "|" + m[4] + "|" + m[5]
	}
	if len(gomap) == 0 {
		t.Fatal("parsed zero bridges from entrypoint.go — the parser has drifted from the source")
	}

	for k, want := range bash {
		got, ok := gomap[k]
		if !ok {
			t.Errorf("bridge %s exists in entrypoint-lib.sh but not in internal/entrypoint", k)
			continue
		}
		if got != want {
			t.Errorf("bridge %s differs\n  bash: %s\n  go:   %s", k, want, got)
		}
	}
	for k := range gomap {
		if _, ok := bash[k]; !ok {
			t.Errorf("bridge %s exists in internal/entrypoint but not in entrypoint-lib.sh", k)
		}
	}
}
