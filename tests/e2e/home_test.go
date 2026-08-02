//go:build e2e

// SPEC: proveo home persistence — durable ~/.proveo session/config mounts.

package e2e

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestProveoHomePersistence asserts durable session/config mounts under
// PROVEO_HOME: plan wiring for every harness, auth scrubbing, resume argv
// forwarding, clean --homes, and (when images exist) live --shell + resume
// round-trips that never bind host IDE homes.
//
//	go test -tags=e2e ./tests/e2e/ -run ProveoHomePersistence -v
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
				if !hasVolume(agentCmd, home, proveohome.ContainerHome) {
					t.Errorf("agent cmd missing proveo home volume %s:%s:\n%s",
						home, proveohome.ContainerHome, agentCmd)
				}
				if !strings.Contains(agentCmd, "HOME="+proveohome.ContainerHome) {
					t.Errorf("agent cmd missing HOME=%s:\n%s", proveohome.ContainerHome, agentCmd)
				}
				if host := os.Getenv("HOME"); host != "" {
					for _, ide := range []string{
						filepath.Join(host, ".cursor"),
						filepath.Join(host, ".claude"),
					} {
						if hasVolumeHost(agentCmd, ide) {
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

	t.Run("auth_json_scrubbed_not_session_marker", func(t *testing.T) {
		home := t.TempDir()
		share := filepath.Join(home, "opencode", "share")
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
		if !strings.Contains(ls, "proveo/cursor:latest ls") {
			t.Errorf("cursor --ls should forward as agent ls:\n%s", ls)
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

	t.Run("live_shell_roundtrip", func(t *testing.T) {
		requireLiveCursorHome(t)
		home, work := dockerVisibleHomeWork(t)
		mustRun(t, work, "git", "init", "-q", ".")
		const mark = "PROVEO-HOME-E2E-OK"
		markerHost := filepath.Join(home, ".cursor", "E2E_HOME_MARK")

		forceClean(proveoBin)
		t.Cleanup(func() {
			forceClean(proveoBin)
			rmByAncestor("proveo/cursor:latest")
		})

		before := dockerIDsByAncestor("proveo/cursor:latest")
		sess := tmux.New(fmt.Sprintf("proveo-home-%d", os.Getpid()), nil)
		t.Cleanup(sess.Kill)

		// env PREFIX injects PROVEO_HOME into the proveo process (same pattern as
		// credentials_test's broker integrity half).
		startCursorLive(t, sess, proveoBin, home, work, "--shell")
		dismissCapabilityPicker(t, sess)

		agentID := waitForNewAncestor(t, "proveo/cursor:latest", before, 120*time.Second, sess)
		src, ok := mountSource(agentID, proveohome.ContainerHome)
		if !ok {
			t.Fatalf("agent %s has no %s mount", agentID, proveohome.ContainerHome)
		}
		if filepath.Clean(src) != filepath.Clean(home) {
			t.Fatalf("proveo home mount source = %q, want %q", src, home)
		}

		// Write from inside the container into the durable home.
		write := exec.Command("docker", "exec", agentID, "bash", "-lc",
			"mkdir -p \"$HOME/.cursor\" && printf '%s' '"+mark+"' > \"$HOME/.cursor/E2E_HOME_MARK\"")
		if out, err := write.CombinedOutput(); err != nil {
			t.Fatalf("docker exec write marker: %v\n%s", err, out)
		}
		waitForFileExists(t, markerHost, 15*time.Second)
		got, err := os.ReadFile(markerHost)
		if err != nil || string(got) != mark {
			t.Fatalf("host marker after write: %v %q", err, got)
		}

		// Kill the container (--rm) and confirm the marker remains on the host.
		_ = exec.Command("docker", "rm", "-f", agentID).Run()
		sess.Kill()
		got, err = os.ReadFile(markerHost)
		if err != nil || string(got) != mark {
			t.Fatalf("marker must survive container --rm: %v %q", err, got)
		}

		// Second run: marker must be visible inside the remounted proveo home.
		before2 := dockerIDsByAncestor("proveo/cursor:latest")
		sess2 := tmux.New(fmt.Sprintf("proveo-home2-%d", os.Getpid()), nil)
		t.Cleanup(sess2.Kill)
		startCursorLive(t, sess2, proveoBin, home, work, "--shell")
		dismissCapabilityPicker(t, sess2)
		agent2 := waitForNewAncestor(t, "proveo/cursor:latest", before2, 120*time.Second, sess2)
		read := exec.Command("docker", "exec", agent2, "bash", "-lc",
			"cat \"$HOME/.cursor/E2E_HOME_MARK\"")
		out, err := read.CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != mark {
			t.Fatalf("second run should see durable marker: err=%v out=%q", err, out)
		}
	})

	t.Run("live_resume_roundtrip", func(t *testing.T) {
		requireLiveCursorHome(t)
		home, work := dockerVisibleHomeWork(t)
		mustRun(t, work, "git", "init", "-q", ".")

		const (
			chatID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
			title  = "PROVEO-RESUME-E2E"
		)
		seedCursorChat(t, home, chatID, title)

		forceClean(proveoBin)
		t.Cleanup(func() {
			forceClean(proveoBin)
			rmByAncestor("proveo/cursor:latest")
		})

		// --ls: agent session picker must list the seeded chat from proveo home.
		func() {
			before := dockerIDsByAncestor("proveo/cursor:latest")
			sess := tmux.New(fmt.Sprintf("proveo-home-ls-%d", os.Getpid()), nil)
			t.Cleanup(sess.Kill)
			startCursorLive(t, sess, proveoBin, home, work, "--ls")
			dismissCapabilityPicker(t, sess)
			_ = waitForNewAncestor(t, "proveo/cursor:latest", before, 120*time.Second, sess)
			if _, err := sess.WaitFor(title, 60*time.Second); err != nil {
				screen, _ := sess.CaptureAll()
				t.Fatalf("--ls should show seeded chat %q: %v\n--- screen ---\n%s", title, err, screen+diagnostics(screen))
			}
			sess.Kill()
			rmByAncestor("proveo/cursor:latest")
		}()

		// --resume <id>: live launch must hand cursor-agent the resume flags.
		func() {
			before := dockerIDsByAncestor("proveo/cursor:latest")
			sess := tmux.New(fmt.Sprintf("proveo-home-resume-%d", os.Getpid()), nil)
			t.Cleanup(sess.Kill)
			startCursorLive(t, sess, proveoBin, home, work, "--resume", chatID)
			dismissCapabilityPicker(t, sess)
			agentID := waitForNewAncestor(t, "proveo/cursor:latest", before, 120*time.Second, sess)
			waitForAgentArgv(t, agentID, 60*time.Second, "--resume", chatID)
			sess.Kill()
			rmByAncestor("proveo/cursor:latest")
		}()

		// --continue: same path for the most-recent session flag.
		func() {
			before := dockerIDsByAncestor("proveo/cursor:latest")
			sess := tmux.New(fmt.Sprintf("proveo-home-cont-%d", os.Getpid()), nil)
			t.Cleanup(sess.Kill)
			startCursorLive(t, sess, proveoBin, home, work, "--continue")
			dismissCapabilityPicker(t, sess)
			agentID := waitForNewAncestor(t, "proveo/cursor:latest", before, 120*time.Second, sess)
			waitForAgentArgv(t, agentID, 60*time.Second, "--continue")
		}()
	})
}

func requireLiveCursorHome(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if !tmux.Available() {
		t.Skip("tmux not installed")
	}
	if !dockerImagePresent(t, "proveo/cursor:latest") {
		t.Skip("proveo/cursor:latest not built")
	}
}

// dockerVisibleHomeWork returns PROVEO_HOME + workspace dirs the Docker daemon
// can bind-mount. In containerized Docker hosts, process /tmp is invisible to
// the daemon; the repo's .cache/e2e path is shared.
func dockerVisibleHomeWork(t *testing.T) (home, work string) {
	t.Helper()
	cache := filepath.Join(repoRoot(t), ".cache", "e2e")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", cache)
	return t.TempDir(), t.TempDir()
}

func startCursorLive(t *testing.T, sess *tmux.Session, proveoBin, home, work string, extra ...string) {
	t.Helper()
	cmd := append([]string{
		"env",
		"PROVEO_HOME=" + home,
		"CURSOR_API_KEY=crsr_test_probe",
		proveoBin, "run", "cursor",
		"--egress-mode", "broker",
	}, extra...)
	cmd = append(cmd, "--input", work)
	if err := sess.Start(200, 40, cmd...); err != nil {
		t.Fatalf("start session: %v", err)
	}
}

// dismissCapabilityPicker accepts the run's choice form. The old add-on picker it
// waited on ("tab to add") was folded into that single form; seeding a cached
// answer does not skip it, because the form always shows the posture being
// launched.
func dismissCapabilityPicker(t *testing.T, sess *tmux.Session) {
	t.Helper()
	acceptChoicePrompt(t, sess, "cursor")
}

// seedCursorChat writes a minimal cursor-agent chat under proveo home, keyed by
// md5("/app") — the workspace path inside every proveo agent container.
func seedCursorChat(t *testing.T, proveoHome, chatID, title string) {
	t.Helper()
	sum := md5.Sum([]byte("/app"))
	dir := filepath.Join(proveoHome, ".cursor", "chats", hex.EncodeToString(sum[:]), chatID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"schemaVersion":1,"createdAtMs":1,"updatedAtMs":1,"hasConversation":true,"title":%q,"cwd":"/app"}`, title)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(repoRoot(t), "tests", "e2e", "testdata", "cursor-empty-store.db")
	in, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read chat store fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "store.db"), in, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForAgentArgv(t *testing.T, containerID string, timeout time.Duration, seq ...string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	want := strings.Join(seq, " ")
	var last string
	for {
		out, err := exec.Command("docker", "exec", containerID, "bash", "-lc",
			"tr '\\0' ' ' </proc/1/cmdline; echo; ps -ww -eo args 2>/dev/null | head -40").CombinedOutput()
		last = string(out)
		if err == nil && containsArgSeq(last, seq...) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent argv missing %q within %s\n--- ps ---\n%s", want, timeout, last)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func runPrintWithHome(t *testing.T, proveoBin, home, target string, extra ...string) string {
	t.Helper()
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

func hasVolume(cmd, host, container string) bool {
	toks := strings.Fields(cmd)
	want := host + ":" + container
	for i := 0; i+1 < len(toks); i++ {
		if toks[i] == "-v" && (toks[i+1] == want || strings.HasPrefix(toks[i+1], want+":")) {
			return true
		}
	}
	return false
}

func hasVolumeHost(cmd, host string) bool {
	toks := strings.Fields(cmd)
	prefix := host + ":"
	for i := 0; i+1 < len(toks); i++ {
		if toks[i] == "-v" && strings.HasPrefix(toks[i+1], prefix) {
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

func dockerIDsByAncestor(image string) map[string]bool {
	out, err := exec.Command("docker", "ps", "-q", "--filter", "ancestor="+image).Output()
	set := map[string]bool{}
	if err != nil {
		return set
	}
	for _, id := range strings.Fields(string(out)) {
		set[id] = true
	}
	return set
}

func waitForNewAncestor(t *testing.T, image string, before map[string]bool, timeout time.Duration, sess *tmux.Session) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for id := range dockerIDsByAncestor(image) {
			if !before[id] {
				return id
			}
		}
		if time.Now().After(deadline) {
			screen, _ := sess.CaptureAll()
			t.Fatalf("no new container for ancestor %s within %s\n--- screen ---\n%s", image, timeout, screen+diagnostics(screen))
		}
		time.Sleep(time.Second)
	}
}
