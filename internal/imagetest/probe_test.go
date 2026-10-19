// SPEC: _spec/tests/testing-strategy.puml
package imagetest

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func hangingDocker(t *testing.T) {
	t.Setenv(PinVar("opencode"), "")
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	prev := ProbeTimeout
	ProbeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { ProbeTimeout = prev })
}

func TestProbesReturnWithinProbeTimeoutOnHungDaemon(t *testing.T) {
	hangingDocker(t)
	for name, probe := range map[string]func() bool{
		"Present":     func() bool { return Present("proveo/opencode:local") },
		"created":     func() bool { _, ok := created("proveo/opencode:local"); return ok },
		"Resolve":     func() bool { return Resolve("opencode", "proveo/opencode:latest") != "proveo/opencode:latest" },
		"DaemonReady": func() bool { return DaemonReady() == nil },
	} {
		start := time.Now()
		if probe() {
			t.Errorf("%s reported success against a hung daemon", name)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("%s took %s, want ≈ProbeTimeout", name, d)
		}
	}
}
