// SPEC: _spec/internal/credentials/credential-decisions.puml
package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTheProjectEnvIsSeededAsADockerEnvFile(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), ".env")
	body := "# project settings\n" +
		"export A=1\n" +
		"B=\"two words\"\n" +
		"C='single'\n" +
		"D=keeps # this hash\n" +
		"ANTHROPIC_API_KEY=sk-ant-fixture\n" +
		"PROVEO_WORKDIR=/elsewhere\n" +
		"1BAD=x\n" +
		"EMPTY=\n"
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name     string
		forwards bool
		want     string
	}{
		{"broker keeps provider keys with sbx's proxy", false, "A=1\nB=two words\nC=single\nD=keeps # this hash\nEMPTY=\n"},
		{"forward carries them", true, "A=1\nANTHROPIC_API_KEY=sk-ant-fixture\nB=two words\nC=single\nD=keeps # this hash\nEMPTY=\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			run := t.TempDir()
			path, n, err := seedProjectEnv(src, run, c.forwards, []string{"PROVEO_WORKDIR=/app", "TERM"})
			if err != nil {
				t.Fatal(err)
			}
			if path != filepath.Join(run, "sbx", "env") {
				t.Errorf("seedProjectEnv path = %q, want <run>/sbx/env", path)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != c.want {
				t.Errorf("seedProjectEnv(%s) wrote\n%s\nwant\n%s", c.name, b, c.want)
			}
			if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
				t.Errorf("the env file is %v, want 0600: it can hold a forwarded key", info.Mode().Perm())
			}
			if wantN := len(splitLines(c.want)); n != wantN {
				t.Errorf("seedProjectEnv counted %d values, want %d", n, wantN)
			}
		})
	}
}

func TestNoProjectEnvSeedsNothing(t *testing.T) {
	t.Parallel()
	for _, src := range []string{"", filepath.Join(t.TempDir(), "missing.env")} {
		run := t.TempDir()
		path, n, err := seedProjectEnv(src, run, false, nil)
		if err != nil || path != "" || n != 0 {
			t.Errorf("seedProjectEnv(%q) = %q, %d, %v; want nothing", src, path, n, err)
		}
		if _, err := os.Stat(filepath.Join(run, "sbx", "env")); err == nil {
			t.Errorf("seedProjectEnv(%q) wrote an env file with nothing in it", src)
		}
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}
