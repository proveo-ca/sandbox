// SPEC: _spec/internal/maintain/image-build-deploy.puml
package maintain

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/proveo-ca/proveo/internal/ui"
)

// captureUI sends ui's status lines to the returned buffer until t ends; its callers never run in parallel.
func captureUI(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	prev := ui.Default
	ui.Default = ui.New(&b)
	t.Cleanup(func() { ui.Default = prev })
	return &b
}

func TestRunFleetPrintGroupsConcurrentAfterBases(t *testing.T) {
	t.Parallel()
	ts := []Target{
		{Name: "base", Kind: KindBase},
		{Name: "base-node", Kind: KindBase},
		{Name: "hermes", Kind: KindHarness},
		{Name: "opencode", Kind: KindHarness},
		{Name: "claudecode", Kind: KindHarness},
		{Name: "claudecode-solidity", Kind: KindHarness},
	}
	var buf bytes.Buffer
	err := RunFleet("build", ts, true, &buf,
		func(Target) {},
		func(tgt Target, pio PlanIO) error {
			fmt.Fprintf(pio.Out, "build %s\n", tgt.Name)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	idxConc := strings.Index(got, "# concurrent ")
	if idxConc < 0 {
		t.Fatalf("print lacks a concurrent group:\n%s", got)
	}
	for _, base := range []string{"# serial base\n", "# serial base-node\n"} {
		idx := strings.Index(got, base)
		if idx < 0 || idx > idxConc {
			t.Errorf("%q is not before the concurrent group:\n%s", strings.TrimSpace(base), got)
		}
	}
	line, _, _ := strings.Cut(got[idxConc:], "\n")
	if strings.Contains(line, "claudecode-solidity") {
		t.Errorf("solidity shares a wave with claudecode:\n%s", line)
	}
	if !strings.Contains(got, "build claudecode-solidity\n") {
		t.Errorf("print lacks the solidity plan line:\n%s", got)
	}
}

func TestSelectTakesANameOrAll(t *testing.T) {
	t.Parallel()
	reg := []Target{{Name: "base"}, {Name: "hermes"}, {Name: "opencode"}}
	names := func(ts []Target) string {
		var out []string
		for _, t := range ts {
			out = append(out, t.Name)
		}
		return strings.Join(out, " ")
	}
	if got, err := Select(reg, "all"); err != nil || names(got) != "base hermes opencode" {
		t.Errorf("Select(all) = %q, %v; want every target", names(got), err)
	}
	if got, err := Select(reg, " hermes "); err != nil || names(got) != "hermes" {
		t.Errorf("Select(hermes) = %q, %v; want hermes", names(got), err)
	}
	for _, c := range []struct{ arg, want string }{
		{"", "no target given; pass a target name or all (targets: base, hermes, opencode)"},
		{"nope", `unknown target "nope" — use 'all' or one of: base, hermes, opencode`},
	} {
		got, err := Select(reg, c.arg)
		if err == nil || err.Error() != c.want || got != nil {
			t.Errorf("Select(%q) = %q, %v; want the error %q", c.arg, names(got), err, c.want)
		}
	}
}

func TestDefsDirPrefersTheOverride(t *testing.T) {
	t.Parallel()
	withDefs := t.TempDir()
	if err := os.Mkdir(filepath.Join(withDefs, "defs"), 0o755); err != nil {
		t.Fatal(err)
	}
	fileDefs := t.TempDir()
	if err := os.WriteFile(filepath.Join(fileDefs, "defs"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "PROVEO_DEFS_DIR" {
				return v
			}
			return ""
		}
	}
	if got, err := DefsDir(env("/elsewhere/defs"), withDefs); err != nil || got != "/elsewhere/defs" {
		t.Errorf("DefsDir with PROVEO_DEFS_DIR = %q, %v; want the override", got, err)
	}
	if got, err := DefsDir(env(""), withDefs); err != nil || got != filepath.Join(withDefs, "defs") {
		t.Errorf("DefsDir in a checkout = %q, %v; want <cwd>/defs", got, err)
	}
	for _, cwd := range []string{t.TempDir(), fileDefs} {
		got, err := DefsDir(env(""), cwd)
		if err == nil || !strings.Contains(err.Error(), "PROVEO_DEFS_DIR") || got != "" {
			t.Errorf("DefsDir(%s) = %q, %v; want an error naming PROVEO_DEFS_DIR", cwd, got, err)
		}
	}
}

// repoDefs is the checkout's defs tree, relative to this package.
var repoDefs = filepath.Join("..", "..", "defs")

func TestTestVerbRunsEachSuiteOnceAndSkipsBases(t *testing.T) {
	status := captureUI(t)
	var out bytes.Buffer
	if err := Fleet("test", "all", "", false, true, repoDefs, &out); err != nil {
		t.Fatal(err)
	}
	var runs []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.Contains(line, "go test ") {
			runs = append(runs, line)
		}
	}
	if len(runs) != 5 {
		t.Errorf("test all prints %d go test lines, want 5 (one per suite):\n%s", len(runs), out.String())
	}
	for _, suite := range []string{"Claudecode", "Codex", "Cursor", "Hermes", "Opencode"} {
		if n := strings.Count(out.String(), " -run ^TestImage"+suite+"$ "); n != 1 {
			t.Errorf("test all runs TestImage%s %d times, want once:\n%s", suite, n, out.String())
		}
	}
	if n := strings.Count(status.String(), "no image suite for "); n != 3 {
		t.Errorf("test all prints %d skip notes, want one per base:\n%s", n, status.String())
	}
	for _, base := range []string{"base", "base-node", "base-node-browser"} {
		if n := strings.Count(status.String(), "no image suite for "+base+" — skipping"); n != 1 {
			t.Errorf("test all notes the %s skip %d times, want once:\n%s", base, n, status.String())
		}
	}

	out.Reset()
	if err := Fleet("test", "codex-browser", "", false, true, repoDefs, &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); strings.Count(got, "\n") != 0 || !strings.Contains(got, "go test -tags=image -count=1 -v -run ^TestImageCodex$ ./internal/imagetest/") {
		t.Errorf("test codex-browser prints %q, want the one codex suite line", got)
	}
}

func TestFleetPrintsTheBuildAndDeployPlans(t *testing.T) {
	status := captureUI(t)
	var out bytes.Buffer
	if err := Fleet("build", "claudecode", "rc1", true, true, repoDefs, &out); err != nil {
		t.Fatal(err)
	}
	want := "# serial claudecode\nimagebuild claudecode --tag rc1 --no-cache\ndocker image inspect proveo/claudecode:rc1\n"
	if out.String() != want {
		t.Errorf("build claudecode --tag rc1 --no-cache prints\n%s\nwant\n%s", out.String(), want)
	}
	if !strings.Contains(status.String(), "building claudecode (proveo/claudecode:rc1)") {
		t.Errorf("build lacks its start line:\n%s", status.String())
	}

	out.Reset()
	if err := Fleet("deploy", "claudecode", "", false, true, repoDefs, &out); err != nil {
		t.Fatal(err)
	}
	want = "# serial claudecode\n" +
		"docker image inspect proveo/claudecode:local\n" +
		"docker tag proveo/claudecode:local proveo/claudecode:latest\n" +
		"imagebuild claudecode --tag latest --push\n"
	if out.String() != want {
		t.Errorf("deploy claudecode prints\n%s\nwant\n%s", out.String(), want)
	}
	if !strings.Contains(status.String(), "deploying proveo/claudecode:latest") {
		t.Errorf("deploy lacks its start line on the default tag:\n%s", status.String())
	}

	if err := Fleet("ship", "claudecode", "", false, true, repoDefs, &out); err == nil {
		t.Error("Fleet accepted the verb ship")
	}
}

func TestPrefixWriterPrefixesEveryLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	pw := &prefixWriter{mu: &sync.Mutex{}, w: &out, prefix: "[codex] "}
	for _, chunk := range []string{"step 1 do", "ne\nstep 2 done\nstep 3 pend"} {
		if n, err := pw.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if got, want := out.String(), "[codex] step 1 done\n[codex] step 2 done\n"; got != want {
		t.Errorf("after two writes = %q, want %q", got, want)
	}
	pw.flush()
	if got, want := out.String(), "[codex] step 1 done\n[codex] step 2 done\n[codex] step 3 pend"; got != want {
		t.Errorf("after flush = %q, want %q", got, want)
	}
	pw.flush()
	if strings.Count(out.String(), "[codex] ") != 3 {
		t.Errorf("a second flush wrote again: %q", out.String())
	}
}
