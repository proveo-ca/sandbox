// SPEC: _spec/cmd/proveo/usage.puml
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

var devAtRevision = regexp.MustCompile(`^proveo version dev@([0-9a-f]{12})(\+dirty)?\n$`)

func TestBuiltBinaryReportsItsVersion(t *testing.T) {
	t.Parallel()
	if strings.Contains(os.Getenv("GOFLAGS"), "-buildvcs=false") {
		t.Skip("GOFLAGS holds -buildvcs=false, so go build stamps no vcs.revision")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go not on PATH: %v", err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH, so go build stamps no vcs.revision: %v", err)
	}
	dir := t.TempDir()
	version := func(ldflags string) string {
		t.Helper()
		bin := filepath.Join(dir, "proveo")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		build := exec.Command(goBin, "build", "-trimpath", "-ldflags="+ldflags, "-o", bin, ".")
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			if strings.Contains(string(out), "error obtaining VCS status") {
				t.Skipf("go build cannot read VCS status here:\n%s", out)
			}
			t.Fatalf("go build -ldflags=%q: %v\n%s", ldflags, err, out)
		}
		run := exec.Command(bin, "--version")
		run.Env = append(os.Environ(), "HOME="+dir, "XDG_CONFIG_HOME="+dir, "XDG_STATE_HOME="+dir, "XDG_CACHE_HOME="+dir)
		out, err := run.Output()
		if err != nil {
			t.Fatalf("proveo --version: %v", err)
		}
		return string(out)
	}

	got := version("-s -w")
	m := devAtRevision.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("an unstamped build reports %q, want proveo version dev@<12 hex>[+dirty] from Go build info", got)
	}
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err == nil && !strings.HasPrefix(string(head), m[1]) {
		t.Errorf("an unstamped build reports revision %s, want HEAD %s", m[1], strings.TrimSpace(string(head)))
	}
	if got := version("-s -w -X main.version=9.9.9"); got != "proveo version 9.9.9\n" {
		t.Errorf("a build stamped -X main.version=9.9.9 reports %q, want the stamp", got)
	}
}

func TestVersionFallsBackToBuildInfo(t *testing.T) {
	t.Parallel()
	const rev = "7a0c8cf4d982299570953843ef2f19c21b38495d"
	info := func(kv ...string) *debug.BuildInfo {
		bi := &debug.BuildInfo{}
		for i := 0; i+1 < len(kv); i += 2 {
			bi.Settings = append(bi.Settings, debug.BuildSetting{Key: kv[i], Value: kv[i+1]})
		}
		return bi
	}
	cases := []struct {
		name, stamped string
		info          *debug.BuildInfo
		ok            bool
		want          string
	}{
		{"a -X stamp wins", "0.2.0", info("vcs.revision", rev, "vcs.modified", "true"), true, "0.2.0"},
		{"a clean revision", "dev", info("vcs.revision", rev, "vcs.modified", "false"), true, "dev@7a0c8cf4d982"},
		{"a modified tree", "dev", info("vcs.revision", rev, "vcs.modified", "true"), true, "dev@7a0c8cf4d982+dirty"},
		{"no revision", "dev", info("vcs.modified", "true"), true, "dev"},
		{"no build info", "dev", nil, false, "dev"},
		{"a short revision stays whole", "dev", info("vcs.revision", "7a0c8cf"), true, "dev@7a0c8cf"},
	}
	for _, c := range cases {
		if got := resolvedVersion(c.stamped, c.info, c.ok); got != c.want {
			t.Errorf("%s: resolvedVersion(%q, …, %v) = %q, want %q", c.name, c.stamped, c.ok, got, c.want)
		}
	}
}
