//go:build e2e

// SPEC: _spec/internal/sbx/ide-attach.puml

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestIDEAttachSSHLandsOnTheCloneNotTheHostTree drives a real clone run, then
// connects the way an IDE's Remote Development does — OpenSSH config, host
// <sid>.sbx — and checks every connection opens the agent's clone: a write
// over SSH reaches the agent's tree and never the host checkout at that path.
func TestIDEAttachSSHLandsOnTheCloneNotTheHostTree(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh client")
	}
	if cfg, err := exec.Command("ssh", "-G", "probe.sbx").Output(); err != nil ||
		!strings.Contains(string(cfg), "ssh proxy") {
		t.Skipf("*.sbx is not routed through sbx; run `%s %s` first", sbx.Binary, strings.Join(sbx.SetupSSHArgs(), " "))
	}
	const target = "claudecode"
	requireHarness(t, target)
	proveoBin := buildProveo(t)

	work, _ := filepath.EvalSymlinks(t.TempDir())
	writeFile(t, filepath.Join(work, "README.md"), []byte("ide attach\n"))
	gitInit(t, work)

	before, canList := sbxSandboxNames()
	sess := tmux.New(fmt.Sprintf("proveo-ideattach-%d", os.Getpid()), nil)
	t.Cleanup(func() {
		sess.Kill()
		removeLeakedSandboxes(t, before, canList)
	})

	cmd := []string{"env"}
	if secrets := harnessSecrets(t, target); len(secrets) > 0 {
		cmd = append(cmd, childEnvArgsFor(t, secrets[0])...)
	} else {
		cmd = append(cmd, childEnvArgs(t)...)
	}
	cmd = append(cmd,
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_AUTO_INSTALL_TOOLS=false",
		proveoBin, "run", target, "--clone", "--shell", "--input", work,
	)
	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start sandbox session: %v", err)
	}

	timeout := durationEnv(t, "PROVEO_TEST_TIMEOUT", 4*time.Minute)
	w := newWatcher(t, sess)
	w.until("the agent shell prompt", timeout, func() bool { return promptReady(w.Screen()) })

	created := newSandboxes(before)
	if len(created) != 1 {
		t.Fatalf("want exactly one new sandbox for this run, got %q", created)
	}
	name := created[0]

	raw, _ := sess.CaptureAll()
	pane := strings.Join(strings.Fields(raw), " ")
	for _, want := range []string{
		"host `" + sbx.SSHHost(name) + "`, use OpenSSH config, project " + work,
		"code --remote ssh-remote+" + sbx.SSHHost(name) + " " + work,
		"a local Open or Recent of " + work + " edits the HOST checkout",
	} {
		if !strings.Contains(pane, want) {
			t.Errorf("interface section lacks %q:\n%s", want, raw)
		}
	}

	probe := `cd ` + quoteWord(work) + ` && printf 'fstype=%s\norigin=%s\n' ` +
		`"$(findmnt -n -o FSTYPE -T .)" "$(git remote get-url origin 2>/dev/null)"`
	for i := range 3 {
		marker := fmt.Sprintf(".ide-attach-%d", i)
		out, err := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=30",
			sbx.SSHHost(name), probe+" && echo "+marker+" > "+marker).CombinedOutput()
		if err != nil {
			t.Fatalf("ssh %s (connection %d): %v\n%s", sbx.SSHHost(name), i, err, out)
		}
		got := string(out)
		if strings.Contains(got, "fstype=virtiofs") || strings.Contains(got, "fstype=fuse") {
			t.Errorf("ssh connection %d opened %s on a host passthrough, not the clone:\n%s", i, work, got)
		}
		if !strings.Contains(got, "origin=/run/sandbox/source") {
			t.Errorf("ssh connection %d did not land in a clone of the host repo:\n%s", i, got)
		}
		if _, err := os.Stat(filepath.Join(work, marker)); err == nil {
			t.Errorf("a write over ssh connection %d reached the host checkout %s", i, work)
		}
		agent, status := shellExec(t, sess, "cat "+marker, 30*time.Second)
		if status != 0 || !strings.Contains(agent, marker) {
			t.Errorf("the agent's tree lacks what ssh connection %d wrote (exit %d):\n%s", i, status, agent)
		}
	}
}
