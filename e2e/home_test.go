//go:build e2e

// SPEC: proveo home persistence — durable ~/.proveo session/config mounts.

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// TestProveoHomePersistence asserts durable session/config mounts under
// PROVEO_HOME: plan wiring for every harness, auth scrubbing, resume argv
// forwarding, and clean --homes.
func TestProveoHomePersistence(t *testing.T) {
	proveoBin := buildProveo(t)

	t.Run("print_plan_mounts_all_agents", func(t *testing.T) {
		t.Parallel()
		agents := []struct {
			target string
			subdir string
		}{
			{"cursor", ".cursor"},
			{"opencode", "opencode"},
			{"claudecode", ".claude"},
			{"codex", ".codex"},
			{"cecli", ".cecli"},
		}
		for _, a := range agents {
			a := a
			t.Run(a.target, func(t *testing.T) {
				t.Parallel()
				home := t.TempDir()
				work := t.TempDir()
				out := runPrintWithHome(t, proveoBin, home, a.target, "--input", work)
				agentCmd := agentCommandLine(t, out)

				if !strings.Contains(out, "proveo home: "+home) {
					t.Errorf("missing proveo home preamble:\n%s", out)
				}
				harnessHome := filepath.Join(home, a.subdir)
				if !strings.Contains(agentCmd, " "+harnessHome) {
					t.Errorf("sbx argv does not carry the harness home dir %s:\n%s", harnessHome, agentCmd)
				}
				if hasSbxWorkspace(agentCmd, home) {
					t.Errorf("sbx argv still mounts the whole proveo home %s; an IDE would read unrelated run logs:\n%s",
						home, agentCmd)
				}
				if !strings.Contains(agentCmd, "-e "+sbx.StateHomeVar+"="+home) {
					t.Errorf("sbx argv missing -e %s=%s — without it the agent's transcripts "+
						"land in volumes teardown removes, and `--resume` has nothing to offer:\n%s",
						sbx.StateHomeVar, home, agentCmd)
				}
				for _, banned := range []string{"-e HOME=", "-e PROVEO_HOME="} {
					if strings.Contains(agentCmd, banned) {
						t.Errorf("sbx argv carries %q — the HOME redirect orphans the proxy-written credential:\n%s",
							banned, agentCmd)
					}
				}
				if host := os.Getenv("HOME"); host != "" {
					for _, ide := range []string{
						filepath.Join(host, ".cursor"),
						filepath.Join(host, ".claude"),
					} {
						if mountsOver(agentCmd, ide) {
							t.Errorf("must not mount host IDE path %s:\n%s", ide, agentCmd)
						}
					}
				}
				subdir := filepath.Join(home, a.subdir)
				if st, err := os.Stat(subdir); err != nil || !st.IsDir() {
					t.Errorf("expected host subdir %s after --print: %v", subdir, err)
				}
			})
		}
	})

	t.Run("login_file_suppresses_that_providers_auth_vars", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		cred := filepath.Join(home, ".claude", ".credentials.json")
		if err := os.MkdirAll(filepath.Dir(cred), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cred, []byte(`{"claudeAiOauth":{"accessToken":"x"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		out := runPrintWithHome(t, proveoBin, home, "claudecode",
			"--input", t.TempDir())
		agentCmd := agentCommandLine(t, out)

		for _, v := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
			if strings.Contains(agentCmd, v) {
				t.Errorf("%s injected over a mounted login; it overrides the file:\n%s", v, agentCmd)
			}
		}
	})

	t.Run("auth_json_scrubbed_not_session_marker", func(t *testing.T) {
		home := t.TempDir()
		share := filepath.Join(home, "opencode", "v2", "share")
		if err := os.MkdirAll(share, 0o700); err != nil {
			t.Fatal(err)
		}
		auth := filepath.Join(share, "auth.json")
		marker := filepath.Join(share, "SESSION_MARK")
		if err := os.WriteFile(auth, []byte(`{"token":"secret"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte("keep-me"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = runPrintWithHome(t, proveoBin, home, "opencode", "--input", t.TempDir())
		if _, err := os.Stat(auth); !os.IsNotExist(err) {
			t.Errorf("auth.json should be scrubbed from proveo home, err=%v", err)
		}
		b, err := os.ReadFile(marker)
		if err != nil || string(b) != "keep-me" {
			t.Errorf("session marker should survive scrub: %v %q", err, b)
		}
	})

	t.Run("resume_argv_forwarding", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		work := t.TempDir()

		cursor := agentCommandLine(t, runPrintWithHome(t, proveoBin, home, "cursor",
			"--resume", "chat-abc", "--input", work))
		if !containsArgSeq(cursor, "--resume", "chat-abc") {
			t.Errorf("cursor --resume not forwarded:\n%s", cursor)
		}

		cont := agentCommandLine(t, runPrintWithHome(t, proveoBin, home, "cursor",
			"--continue", "--input", work))
		if !containsArgSeq(cont, "--continue") {
			t.Errorf("cursor --continue not forwarded:\n%s", cont)
		}

		ls := agentCommandLine(t, runPrintWithHome(t, proveoBin, home, "cursor",
			"--ls", "--input", work))
		if !strings.HasSuffix(strings.TrimSpace(ls), " ls") {
			t.Errorf("cursor --ls should forward as the agent command ls:\n%s", ls)
		}

		oc := agentCommandLine(t, runPrintWithHome(t, proveoBin, home, "opencode",
			"--resume", "sess-1", "--input", work))
		if !containsArgSeq(oc, "--session", "sess-1") {
			t.Errorf("opencode --resume should map to --session:\n%s", oc)
		}

		claude := agentCommandLine(t, runPrintWithHome(t, proveoBin, home, "claudecode",
			"--resume", "ulid-1", "--input", work))
		if !containsArgSeq(claude, "--resume", "ulid-1") {
			t.Errorf("claudecode --resume not forwarded:\n%s", claude)
		}
	})

	t.Run("clean_homes", func(t *testing.T) {
		home := t.TempDir()
		marker := filepath.Join(home, ".cursor", "x")
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(proveoBin, "clean", "--homes", "--dry-run")
		cmd.Env = append(os.Environ(), "PROVEO_HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("clean --homes --dry-run: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), home) {
			t.Errorf("dry-run should mention proveo home path:\n%s", out)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("dry-run must not delete: %v", err)
		}
		cmd = exec.Command(proveoBin, "clean", "--homes")
		cmd.Env = append(os.Environ(), "PROVEO_HOME="+home)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("clean --homes: %v\n%s", err, out)
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Errorf("clean --homes should remove PROVEO_HOME, err=%v", err)
		}
	})

}

func runPrintWithHome(t *testing.T, proveoBin, home, target string, extra ...string) string {
	t.Helper()
	requireSbx(t)
	args := append([]string{"run", target}, extra...)
	args = append(args, "--print")
	cmd := exec.Command(proveoBin, args...)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(envWithoutProviderKeys(),
		"PROVEO_HOME="+home,
		"CURSOR_API_KEY=crsr_test_probe",
		"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-test-probe",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func hasSbxWorkspace(argv, host string) bool {
	for _, f := range strings.Fields(argv) {
		path, _, _ := strings.Cut(f, ":")
		if path == host {
			return true
		}
	}
	return false
}

// mountsOver reports whether any absolute workspace path in argv is path or a parent of it.
func mountsOver(argv, path string) bool {
	for _, f := range strings.Fields(argv) {
		ws, _, _ := strings.Cut(f, ":")
		if filepath.IsAbs(ws) && pathWithin(path, ws) {
			return true
		}
	}
	return false
}

func containsArgSeq(cmd string, seq ...string) bool {
	toks := strings.Fields(cmd)
	for i := 0; i+len(seq) <= len(toks); i++ {
		ok := true
		for j := range seq {
			if toks[i+j] != seq[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
