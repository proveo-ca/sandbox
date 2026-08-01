// Package verify discovers project verification commands (test/lint/build/…).
// It replaces defs/lib/detect-verify.sh as the single source of truth.
//
// SPEC: _spec/internal/verify/verification-discovery.puml, _spec/defs/cursor/cursor-paradigm.puml
package verify

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Command is one discovered verification action.
type Command struct {
	Category string // test | lint | build | typecheck | fmt
	Cmd      string // shell command to run
}

// Line formats as category|command (bash detect_verify_commands shape).
func (c Command) Line() string {
	return c.Category + "|" + c.Cmd
}

// Detect returns verification commands for root, matching the former bash helper.
// lookPath may be exec.LookPath; nil uses exec.LookPath.
func Detect(root string, lookPath func(string) (string, error)) []Command {
	if root == "" {
		root, _ = os.Getwd()
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return nil
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	var out []Command
	out = append(out, detectNode(root)...)
	out = append(out, detectPython(root, lookPath)...)
	if exists(filepath.Join(root, "go.mod")) {
		out = append(out,
			Command{Category: "test", Cmd: "go test ./..."},
			Command{Category: "build", Cmd: "go build ./..."},
		)
	}
	if exists(filepath.Join(root, "Cargo.toml")) {
		out = append(out,
			Command{Category: "test", Cmd: "cargo test"},
			Command{Category: "build", Cmd: "cargo build"},
			Command{Category: "lint", Cmd: "cargo clippy"},
		)
	}
	if exists(filepath.Join(root, "Dockerfile")) || exists(filepath.Join(root, "docker-compose.yml")) {
		out = append(out, Command{Category: "build", Cmd: "docker build ."})
	}
	return out
}

// FormatLines joins Detect results as category|command lines (no trailing newline on last optional).
func FormatLines(cmds []Command) string {
	if len(cmds) == 0 {
		return ""
	}
	var b strings.Builder
	for i, c := range cmds {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(c.Line())
	}
	return b.String()
}

func detectNode(root string) []Command {
	pkgPath := filepath.Join(root, "package.json")
	if !exists(pkgPath) {
		return nil
	}
	runner, testCmd := "npm run", "npm test"
	if exists(filepath.Join(root, "pnpm-lock.yaml")) {
		runner, testCmd = "pnpm", "pnpm test"
	} else if exists(filepath.Join(root, "yarn.lock")) {
		runner, testCmd = "yarn", "yarn test"
	}

	scripts := nodeScriptKeys(pkgPath)
	var out []Command
	if scripts["test"] {
		out = append(out, Command{Category: "test", Cmd: testCmd})
	}
	if scripts["lint"] {
		out = append(out, Command{Category: "lint", Cmd: runner + " lint"})
	}
	if scripts["build"] {
		out = append(out, Command{Category: "build", Cmd: runner + " build"})
	}
	if scripts["typecheck"] {
		out = append(out, Command{Category: "typecheck", Cmd: runner + " typecheck"})
	}
	if scripts["fmt"] || scripts["format"] {
		out = append(out, Command{Category: "fmt", Cmd: runner + " fmt"})
	}
	return out
}

func nodeScriptKeys(pkgPath string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(pkgPath)
	if err != nil {
		return out
	}
	var doc struct {
		Scripts map[string]json.RawMessage `json:"scripts"`
	}
	if err := json.Unmarshal(b, &doc); err != nil || doc.Scripts == nil {
		// Fallback: substring match like bash without jq.
		s := string(b)
		for _, k := range []string{"test", "lint", "build", "typecheck", "fmt", "format"} {
			if strings.Contains(s, `"`+k+`"`) {
				out[k] = true
			}
		}
		return out
	}
	for k := range doc.Scripts {
		out[k] = true
	}
	return out
}

func detectPython(root string, lookPath func(string) (string, error)) []Command {
	if !exists(filepath.Join(root, "pyproject.toml")) &&
		!exists(filepath.Join(root, "setup.py")) &&
		!exists(filepath.Join(root, "requirements.txt")) {
		return nil
	}
	var out []Command
	if _, err := lookPath("pytest"); err == nil {
		out = append(out, Command{Category: "test", Cmd: "pytest"})
	} else if err := exec.Command("python3", "-c", "import pytest").Run(); err == nil {
		out = append(out, Command{Category: "test", Cmd: "pytest"})
	}
	if _, err := lookPath("ruff"); err == nil {
		out = append(out, Command{Category: "lint", Cmd: "ruff check ."})
	}
	if _, err := lookPath("mypy"); err == nil {
		out = append(out, Command{Category: "typecheck", Cmd: "mypy ."})
	}
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
