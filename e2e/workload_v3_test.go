//go:build e2e

// SPEC: _spec/internal/sbx/sbx-kit-v3.puml
package e2e

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// probeWorkload writes a minimal v3 workload named agent: the recipe r, sbx@1 first, then caps.
func probeWorkload(t *testing.T, agent string, r sbx.RecipeV3, caps ...sbx.CapabilityV3) string {
	t.Helper()
	w := sbx.WorkloadV3{
		DisplayName:  agent + " (proveo e2e probe)",
		Version:      "1.0.0",
		Capabilities: append([]sbx.CapabilityV3{{Type: sbx.CapSbx}}, caps...),
	}
	dir, err := sbx.WriteWorkloadV3(sbx.KitDirV3(t.TempDir(), agent), w, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// createWorkloadSandbox runs `sbx create` for cfg (cfg.KitDir set) and removes the sandbox at cleanup.
// It skips the test when sbx refuses the create.
func createWorkloadSandbox(t *testing.T, cfg sbx.RunConfig) {
	t.Helper()
	t.Cleanup(func() {
		if out, err := exec.Command(sbx.Binary, sbx.RemoveArgs(cfg.Name)...).CombinedOutput(); err != nil && !sbx.NotFound(string(out)) {
			t.Logf("probe sandbox %s not removed: %v\n%s", cfg.Name, err, out)
		}
	})
	if out, err := exec.Command(sbx.Binary, sbx.CreateArgs(cfg)...).CombinedOutput(); err != nil {
		t.Skipf("sbx create %s: %v\n%s", cfg.Name, err, out)
	}
}

// mountsOf turns host paths into positional workspace mounts.
func mountsOf(hosts ...string) []sbx.Mount {
	out := make([]sbx.Mount, len(hosts))
	for i, h := range hosts {
		out[i] = sbx.Mount{Host: h}
	}
	return out
}

// shellWords splits a line sbx.ShellJoin wrote back into argv.
func shellWords(line string) []string {
	var out []string
	var cur strings.Builder
	inWord, quoted := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quoted && c == '\'':
			quoted = false
		case quoted:
			cur.WriteByte(c)
		case c == '\'':
			quoted, inWord = true, true
		case c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
			inWord = true
		case c == ' ' || c == '\t':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}

// printedLine is the command line under marker ("# sandbox" or "# agent") in --print output.
func printedLine(t *testing.T, out, marker string) string {
	t.Helper()
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == marker && i+1 < len(lines) {
			return lines[i+1]
		}
	}
	t.Fatalf("no %q command in --print output:\n%s", marker, out)
	return ""
}

// printedKitDir is the workload directory --print reports writing.
func printedKitDir(t *testing.T, out string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if _, rest, ok := strings.Cut(l, "sandbox kit: "); ok {
			return strings.Fields(rest)[0]
		}
	}
	t.Fatalf("--print reported no sandbox kit:\n%s", out)
	return ""
}

func TestShellWordsReadsWhatShellJoinWrote(t *testing.T) {
	t.Parallel()
	argv := []string{"create", "-e", "GIT_AUTHOR_NAME=Ada Lovelace", "-e", "D=.claude|jobs/*/tmp", "-e", "Q=it's", "/w"}
	if got := shellWords(sbx.ShellJoin(argv)); strings.Join(got, "\x00") != strings.Join(argv, "\x00") {
		t.Errorf("shellWords(ShellJoin(%q)) = %q", argv, got)
	}
}
