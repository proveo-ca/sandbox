// SPEC: _spec/internal/cdn/distribution-update.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var installerScripts = []string{
	"apps/cli/public/cli/install.sh",
	"apps/cli/public/cli/uninstall.sh",
}

var installerBash4Rules = []struct {
	name string
	re   *regexp.Regexp
}{
	{"mapfile/readarray", regexp.MustCompile(`\b(mapfile|readarray)\b`)},
	{"associative array", regexp.MustCompile(`\b(declare|local|typeset)\s+-[a-zA-Z]*A`)},
	{"nameref or global declare", regexp.MustCompile(`\b(declare|local|typeset)\s+-[a-zA-Z]*[ng]`)},
	{"case modification", regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(\[[^]]*\])?(,,?|\^\^?)[^}]*\}`)},
	{"parameter transformation", regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(\[[^]]*\])?@[QEPAKaUuL]\}`)},
	{"coproc", regexp.MustCompile(`\bcoproc\b`)},
	{"&>> append", regexp.MustCompile(`&>>`)},
	{"|& pipe", regexp.MustCompile(`\|&`)},
	{"case fallthrough", regexp.MustCompile(`;;&|[^;];&`)},
	{"globstar", regexp.MustCompile(`shopt\s+-s\s+[^#]*\bglobstar\b`)},
	{"wait -n", regexp.MustCompile(`\bwait\s+-n\b`)},
	{"EPOCHSECONDS/EPOCHREALTIME", regexp.MustCompile(`\bEPOCH(SECONDS|REALTIME)\b`)},
	{"negative array index", regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\[-[0-9]+\]\}`)},
}

var (
	setsNounset   = regexp.MustCompile(`(?m)^\s*set\s+(-[a-zA-Z]*u[a-zA-Z]*\b|-o\s+nounset\b)`)
	guardedArray  = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\[@\]\+"\$\{([A-Za-z_][A-Za-z0-9_]*)\[@\]\}"\}`)
	arrayExpanded = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\[@\]\}`)
)

func readInstaller(t *testing.T, rel string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(string(b), "\n")
}

// install.sh runs as `curl | bash` under macOS /bin/bash 3.2.
func TestInstallerParsesUnderSystemBash(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skipf("/bin/bash absent: %v", err)
	}
	for _, rel := range installerScripts {
		out, err := exec.Command("/bin/bash", "-n", filepath.Join(repoRoot(t), rel)).CombinedOutput()
		if err != nil {
			t.Errorf("/bin/bash -n %s: %v\n%s", rel, err, out)
		}
	}
}

func TestInstallerAvoidsBash4Features(t *testing.T) {
	t.Parallel()
	for _, rel := range installerScripts {
		for i, line := range readInstaller(t, rel) {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			for _, r := range installerBash4Rules {
				if m := r.re.FindString(line); m != "" {
					t.Errorf("%s:%d uses %s (%q), which macOS /bin/bash 3.2 lacks", rel, i+1, r.name, m)
				}
			}
		}
	}
}

// bash < 4.4 treats "${arr[@]}" of an empty array as unbound under `set -u`.
func TestInstallerGuardsArrayExpansionUnderNounset(t *testing.T) {
	t.Parallel()
	for _, rel := range installerScripts {
		lines := readInstaller(t, rel)
		if !setsNounset.MatchString(strings.Join(lines, "\n")) {
			continue
		}
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			for _, g := range guardedArray.FindAllStringSubmatch(line, -1) {
				if g[1] != g[2] {
					t.Errorf("%s:%d guard names %s but expands %s", rel, i+1, g[1], g[2])
				}
			}
			for _, m := range arrayExpanded.FindAllString(guardedArray.ReplaceAllString(line, ""), -1) {
				t.Errorf("%s:%d expands %s unguarded under set -u; write ${X[@]+\"${X[@]}\"}", rel, i+1, m)
			}
		}
	}
}
