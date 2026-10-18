// SPEC: _spec/internal/envflag/env-switches.puml
package contract_test

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/envflag"
)

// shellSwitchHelpers are the lib functions that define the on/off vocabulary.
var shellSwitchHelpers = map[string]bool{
	"_proveo_env_word": true, "proveo_env_on": true, "proveo_env_off": true, "proveo_env_enabled": true,
}

// shellSwitchExempt maps a file to the PROVEO_* switches it may still parse by hand.
var shellSwitchExempt = map[string]map[string]string{
	"packages/lib/hooks/git-sync-turn.sh": {
		"PROVEO_GIT_SYNC":              "standalone hook; the base image ships it without the lib",
		"PROVEO_GIT_SYNC_MSG":          "standalone hook; the base image ships it without the lib",
		"PROVEO_GIT_SYNC_MSG_INFLIGHT": "standalone hook; the base image ships it without the lib",
		"PROVEO_GIT_SYNC_TRACE":        "standalone hook; a path or the enum word off",
	},
	"packages/lib/proveo-await-seed": {
		"PROVEO_SEED_PROGRESS": "POSIX sh; cannot source the bash lib",
	},
}

func TestShellEnvSwitchHelperMatchesEnvflag(t *testing.T) {
	t.Parallel()
	words := []string{"1", "true", "yes", "on", "enable", "enabled", "0", "false", "no", "off", "disable", "disabled"}
	var values []string
	for _, w := range words {
		values = append(values, w, strings.ToUpper(w), alternateCase(w), " "+w+" ", "\t"+strings.ToUpper(w)+"\n", "\v"+alternateCase(w)+"\f")
	}
	values = append(values, "", " ", "\t", "auto", "2", "-1", "maybe", "onn", "o n", "of", "y", "n", "truee", "enabledd", "1 1")

	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	names := make([]string, 0, len(values)+1)
	for i, v := range values {
		names = append(names, fmt.Sprintf("SW_%d", i))
		env = append(env, fmt.Sprintf("SW_%d=%s", i, v))
	}
	names = append(names, "SW_UNSET")

	script := `set -euo pipefail
source "$1/packages/lib/entrypoint-lib.sh"
shift
REPLY=keep
for n in "$@"; do
  a=0 b=0 c=0 d=0
  if proveo_env_on "$n"; then a=1; fi
  if proveo_env_off "$n"; then b=1; fi
  if proveo_env_enabled "$n" on; then c=1; fi
  if proveo_env_enabled "$n" off; then d=1; fi
  printf 'RESULT %s %s%s%s%s\n' "$n" "$a" "$b" "$c" "$d"
done
printf 'REPLY %s\n' "$REPLY"`
	cmd := exec.Command(bashOrSkip(t), append([]string{"-c", script, "bash", repoRoot(t)}, names...)...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash helper run failed: %v\n%s", err, out)
	}
	got := map[string]string{}
	reply := ""
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 3 && f[0] == "RESULT":
			got[f[1]] = f[2]
		case len(f) == 2 && f[0] == "REPLY":
			reply = f[1]
		}
	}
	if reply != "keep" {
		t.Errorf("the helpers clobbered the caller's REPLY: %q", reply)
	}
	bit := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	for i, name := range names {
		v := ""
		if i < len(values) {
			v = values[i]
		}
		want := bit(envflag.On(v)) + bit(envflag.Off(v)) + bit(envflag.Bool(v, true)) + bit(envflag.Bool(v, false))
		if got[name] != want {
			t.Errorf("%q: bash on/off/enabled(on)/enabled(off) = %q, envflag = %q", v, got[name], want)
		}
	}
}

func alternateCase(w string) string {
	b := []byte(w)
	for i := 0; i < len(b); i += 2 {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 'a' - 'A'
		}
	}
	return string(b)
}

// TestShellEnvSwitchesParseThroughTheLibHelper fails on a hand-rolled on/off parse of a PROVEO_* switch.
func TestShellEnvSwitchesParseThroughTheLibHelper(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	used := map[string]bool{}
	for _, dir := range []string{"packages/lib", "defs"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			first, _, _ := bytes.Cut(b, []byte("\n"))
			if !strings.HasSuffix(path, ".sh") && !shellShebang.Match(first) {
				return nil
			}
			rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
			for _, hit := range shellSwitchFindings(string(b)) {
				if _, ok := shellSwitchExempt[rel][hit.name]; ok {
					used[rel+" "+hit.name] = true
					continue
				}
				t.Errorf("%s:%d: %s compared with an on/off word — use proveo_env_on/off/enabled: %s",
					rel, hit.line, hit.name, strings.TrimSpace(hit.text))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var stale []string
	for rel, names := range shellSwitchExempt {
		for name := range names {
			if !used[rel+" "+name] {
				stale = append(stale, rel+" "+name)
			}
		}
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Errorf("shellSwitchExempt lists %s, which no longer parses it by hand — drop the entry", s)
	}
}

func TestShellEnvSwitchScanFlagsHandRolledParsers(t *testing.T) {
	t.Parallel()
	flagged := map[string]string{
		"double bracket": `if [[ "${PROVEO_X:-0}" == "1" ]]; then echo on; fi`,
		"single bracket": `[ "${PROVEO_X:-}" = 1 ] || exit 0`,
		"not equal":      `[[ $PROVEO_X != TRUE ]] && exit 0`,
		"numeric":        `if [ "${PROVEO_X:-0}" -eq 0 ]; then :; fi`,
		"second clause":  `[[ "$target" == opencode && "${PROVEO_X:-}" == 1 ]] || return 0`,
		"case one line":  `case "$(printf '%s' "${PROVEO_X:-on}" | tr A-Z a-z)" in off|false) return 0 ;; esac`,
		"case block":     "case \"$PROVEO_X\" in\n  auto) ;;\n  OFF | No) return 0 ;;\nesac",
		"tainted local":  "f() {\n  local mode\n  mode=\"$(printf '%s' \"${PROVEO_X:-auto}\" | tr A-Z a-z)\"\n  case \"$mode\" in off) return 0 ;; esac\n}",
		"tainted chain":  "f() {\n  local a=\"${PROVEO_X:-}\" b\n  b=\"$a\"\n  [[ \"$b\" == yes ]]\n}",
	}
	clean := map[string]string{
		"presence":        `[[ -n "${PROVEO_X:-}" ]] && echo set`,
		"enum word":       `[[ "${PROVEO_X:-}" != darwin ]]`,
		"enum case":       "case \"${PROVEO_X:-auto}\" in\n  auto) ;;\n  always) ;;\nesac",
		"helper call":     `proveo_env_on PROVEO_X && echo on`,
		"not proveo":      `[[ "$HERMES_LOCAL_WIRED" != 1 ]]`,
		"json value":      `case "$(_json_get stop_hook_active)" in true|1) continued=1 ;; esac`,
		"helper body":     "proveo_env_on() {\n  [[ \"${PROVEO_X:-}\" == 1 ]]\n}",
		"other function":  "f() {\n  mode=\"${PROVEO_X:-}\"\n}\ng() {\n  case \"$mode\" in off) ;; esac\n}",
		"reassigned":      "f() {\n  local mode=\"${PROVEO_X:-}\"\n  mode=\"${1:-}\"\n  [[ \"$mode\" == off ]]\n}",
		"other local":     "f() {\n  local mode=\"${1:-}\" host=\"${PROVEO_X:-}\"\n  [[ \"$mode\" == 1 ]]\n}",
		"comment":         `# [[ "${PROVEO_X:-0}" == 1 ]] was the old spelling`,
		"value passthru":  `LAUNCH_ARGS=(--sandbox "${PROVEO_X:-disabled}")`,
		"assignment only": `PROVEO_X=1 some_command`,
	}
	for name, src := range flagged {
		if got := shellSwitchFindings(src); len(got) == 0 {
			t.Errorf("%s: scan missed\n%s", name, src)
		}
	}
	for name, src := range clean {
		if got := shellSwitchFindings(src); len(got) != 0 {
			t.Errorf("%s: scan flagged %+v in\n%s", name, got, src)
		}
	}
}

type shellSwitchHit struct {
	line       int
	name, text string
}

var (
	shellFuncDef   = regexp.MustCompile(`^\s*(?:function\s+)?([A-Za-z_][\w-]*)\s*\(\)`)
	shellAssign    = regexp.MustCompile(`(?:^|[\s;(&|])([A-Za-z_]\w*)=`)
	shellProveoRef = regexp.MustCompile(`\$\{?(PROVEO_[A-Z0-9_]+)`)
	shellVarRef    = regexp.MustCompile(`\$\{?([A-Za-z_]\w*)`)
	shellCompare   = regexp.MustCompile(`("[^"]*"|'[^']*'|[^\s"'\[\]()]+)\s+(==|!=|=|-eq|-ne)\s+("[^"]*"|'[^']*'|[^\s"'\[\]();&|]+)`)
	shellCaseHead  = regexp.MustCompile(`\bcase\s+(.+?)\s+in\b(.*)$`)
	shellCaseArm   = regexp.MustCompile(`^\s*\(?\s*([^()]*?)\s*\)`)
	shellEsac      = regexp.MustCompile(`\besac\b`)
)

// shellSwitchFindings returns each line that compares a PROVEO_* value with an on/off word.
func shellSwitchFindings(src string) []shellSwitchHit {
	lines := strings.Split(src, "\n")
	var hits []shellSwitchHit
	taint := map[string]string{}
	inHelper := false
	for i, line := range lines {
		if inHelper {
			inHelper = !strings.HasPrefix(line, "}")
			continue
		}
		if m := shellFuncDef.FindStringSubmatch(line); m != nil {
			taint = map[string]string{}
			if shellSwitchHelpers[m[1]] {
				inHelper = !strings.HasSuffix(strings.TrimSpace(line), "}")
				continue
			}
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		locs := shellAssign.FindAllStringSubmatchIndex(line, -1)
		for _, l := range locs {
			val := line[l[1]:shellWordEnd(line, l[1])]
			if src := shellValueSource(val, taint); src != "" {
				taint[line[l[2]:l[3]]] = src
			} else {
				delete(taint, line[l[2]:l[3]])
			}
		}
		for _, m := range shellCompare.FindAllStringSubmatch(line, -1) {
			for _, pair := range [][2]string{{m[1], m[3]}, {m[3], m[1]}} {
				if src := shellValueSource(pair[0], taint); src != "" && envSwitchWords[shellUnquote(pair[1])] {
					hits = append(hits, shellSwitchHit{i + 1, src, line})
				}
			}
		}
		if m := shellCaseHead.FindStringSubmatch(line); m != nil {
			if src := shellValueSource(m[1], taint); src != "" {
				for _, n := range shellCaseArmHits(lines, i, m[2]) {
					hits = append(hits, shellSwitchHit{n + 1, src, lines[n]})
				}
			}
		}
	}
	return hits
}

// shellCaseArmHits returns the lines of a case block whose arm lists an on/off word.
func shellCaseArmHits(lines []string, head int, rest string) []int {
	var out []int
	depth := 1
	for n := head; n < len(lines) && depth > 0; n++ {
		text := rest
		if n > head {
			text = lines[n]
			if shellCaseHead.MatchString(text) {
				depth++
			}
		}
		for _, piece := range strings.Split(text, ";;") {
			if depth != 1 {
				break
			}
			m := shellCaseArm.FindStringSubmatch(piece)
			if m == nil {
				continue
			}
			for _, alt := range strings.Split(m[1], "|") {
				if envSwitchWords[shellUnquote(alt)] {
					out = append(out, n)
					break
				}
			}
		}
		depth -= len(shellEsac.FindAllString(text, -1))
	}
	return out
}

func shellValueSource(s string, taint map[string]string) string {
	if m := shellProveoRef.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	for _, m := range shellVarRef.FindAllStringSubmatch(s, -1) {
		if src, ok := taint[m[1]]; ok {
			return src
		}
	}
	return ""
}

func shellUnquote(s string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(s), `"'`))
}

// shellWordEnd returns the index just past the shell word that starts at s[i].
func shellWordEnd(s string, i int) int {
	for i < len(s) {
		switch c := s[i]; {
		case c == '\\':
			i += 2
		case c == '\'':
			i = shellQuoteEnd(s, i+1, '\'')
		case c == '"':
			i = shellDoubleEnd(s, i+1)
		case strings.HasPrefix(s[i:], "$("):
			i = shellNestEnd(s, i+2, '(', ')')
		case strings.HasPrefix(s[i:], "${"):
			i = shellNestEnd(s, i+2, '{', '}')
		case strings.ContainsRune(" \t;&|)<>", rune(c)):
			return i
		default:
			i++
		}
	}
	return len(s)
}

func shellQuoteEnd(s string, i int, q byte) int {
	if j := strings.IndexByte(s[i:], q); j >= 0 {
		return i + j + 1
	}
	return len(s)
}

func shellDoubleEnd(s string, i int) int {
	for i < len(s) {
		switch {
		case s[i] == '\\':
			i += 2
		case s[i] == '"':
			return i + 1
		case strings.HasPrefix(s[i:], "$("):
			i = shellNestEnd(s, i+2, '(', ')')
		case strings.HasPrefix(s[i:], "${"):
			i = shellNestEnd(s, i+2, '{', '}')
		default:
			i++
		}
	}
	return len(s)
}

func shellNestEnd(s string, i int, open, closing byte) int {
	depth := 1
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
		case '\'':
			i = shellQuoteEnd(s, i+1, '\'')
		case '"':
			i = shellDoubleEnd(s, i+1)
		case open:
			depth++
			i++
		case closing:
			depth--
			i++
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(s)
}
