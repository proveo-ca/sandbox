//go:build !windows

// SPEC: _spec/internal/sbx/clone-workspace.puml
package sandbox

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// fakeSbx puts an sbx on PATH that lists sb as stopped, answers the clone-mode
// probe with mode, exits snapshot from the snapshot and bundle from the bundle,
// and logs every call; it returns the log's path.
func fakeSbx(t *testing.T, mode string, snapshot, bundle int) string {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> '" + calls + "'\n" +
		"case \"$*\" in\n" +
		"  ls*) printf 'SANDBOX AGENT STATUS\\nsb claude stopped\\n' ;;\n" +
		"  */run/sandbox/source*) echo " + mode + " ;;\n" +
		"  *'git add -A'*) exit " + strconv.Itoa(snapshot) + " ;;\n" +
		"  *'bundle create'*) echo 'bundle refused' >&2; exit " + strconv.Itoa(bundle) + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, sbx.Binary), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	orig := receiptDir
	t.Cleanup(func() { receiptDir = orig })
	receiptDir = func() string { return bin }
	return calls
}

func TestRetiringASandboxKeepsItWhenTheCloneCouldNotBeCarriedHome(t *testing.T) {
	for _, c := range []struct {
		name         string
		guest        string
		thisRunClone bool
		snapshot     int
		bundle       int
		wantErr      bool
		wantSnapshot bool
		wantRemove   bool
	}{
		{"the bundle failed", "clone", true, 0, 1, true, true, false},
		{"the snapshot failed", "clone", true, 1, 0, true, true, false},
		{"the clone held nothing new", "clone", true, 0, sbx.CloneBundleEmpty, false, true, true},
		{"a clone sandbox retired by a --clone=false run", "clone", false, 0, 1, true, true, false},
		{"a direct sandbox retired by a clone run", "direct", true, 0, 0, false, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := fakeSbx(t, c.guest, c.snapshot, c.bundle)
			repo := t.TempDir()
			in := Input{Clone: c.thisRunClone, RepoRoot: repo, Sid: "proveo-1-2"}
			cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: repo}}}

			err := retireSandbox(in, cfg)
			b, _ := os.ReadFile(calls)
			log := string(b)
			if (err != nil) != c.wantErr {
				t.Errorf("retireSandbox(%s) error = %v, want error %v", c.name, err, c.wantErr)
			}
			if got := strings.Contains(log, "git add -A"); got != c.wantSnapshot {
				t.Errorf("retireSandbox(%s) snapshotted = %v, want %v\ncalls:\n%s", c.name, got, c.wantSnapshot, log)
			}
			if got := strings.Contains(log, "rm --force sb"); got != c.wantRemove {
				t.Errorf("retireSandbox(%s) ran sbx rm = %v, want %v\ncalls:\n%s", c.name, got, c.wantRemove, log)
			}
		})
	}
}
