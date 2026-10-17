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

// fakeSbxBundle puts an sbx on PATH that lists sb as stopped, answers the bundle
// with exit status bundle, and logs every call; it returns the log's path.
func fakeSbxBundle(t *testing.T, bundle int) string {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> '" + calls + "'\n" +
		"case \"$*\" in\n" +
		"  ls*) printf 'SANDBOX AGENT STATUS\\nsb claude stopped\\n' ;;\n" +
		"  *'bundle create'*) echo 'bundle refused' >&2; exit " + strconv.Itoa(bundle) + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, sbx.Binary), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func TestRetiringASandboxKeepsItWhenTheCloneCouldNotBeCarriedHome(t *testing.T) {
	for _, c := range []struct {
		name       string
		bundle     int
		wantErr    bool
		wantRemove bool
	}{
		{"the bundle failed", 1, true, false},
		{"the clone held nothing new", sbx.CloneBundleEmpty, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := fakeSbxBundle(t, c.bundle)
			repo := t.TempDir()
			in := Input{Clone: true, RepoRoot: repo, Sid: "proveo-1-2"}
			cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: repo}}}

			err := retireSandbox(in, cfg)
			log, _ := os.ReadFile(calls)
			removed := strings.Contains(string(log), "rm --force sb")
			if (err != nil) != c.wantErr {
				t.Errorf("retireSandbox(%s) error = %v, want error %v", c.name, err, c.wantErr)
			}
			if removed != c.wantRemove {
				t.Errorf("retireSandbox(%s) ran sbx rm = %v, want %v\ncalls:\n%s", c.name, removed, c.wantRemove, log)
			}
		})
	}
}
