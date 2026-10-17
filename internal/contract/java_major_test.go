// SPEC: _spec/_runtimes/toolchain-provisioning.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestJavaMajorReadsLegacyAndModernVersionStrings(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	for _, c := range []struct {
		version string
		want    string
	}{
		{`openjdk version "1.8.0_402"`, "8"},
		{`java version "1.8.0_202"`, "8"},
		{`openjdk version "17" 2021-09-14`, "17"},
		{`openjdk version "21.0.4" 2024-07-16`, "21"},
	} {
		t.Run(c.version, func(t *testing.T) {
			t.Parallel()
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "java"), []byte("#!/bin/sh\necho '"+c.version+"' >&2\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bash, "-c", `source "$1/packages/lib/entrypoint-lib.sh"; _java_major`, "bash", repoRoot(t))
			cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "HOME="+t.TempDir())
			out, err := cmd.Output()
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if got := lines[len(lines)-1]; err != nil || got != c.want {
				t.Errorf("_java_major with java -version %q = %q (%v), want %s", c.version, got, err, c.want)
			}
		})
	}
}
