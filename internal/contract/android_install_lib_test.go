// SPEC: _spec/internal/devports/dev-ports.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// androidInstall runs proveo_android_install in a workspace with gradle and adb stubbed.
func androidInstall(t *testing.T, ready bool, gradleRC string, args ...string) (out, calls string) {
	t.Helper()
	bash := bashOrSkip(t)
	bin, work, tmp := t.TempDir(), t.TempDir(), t.TempDir()
	log := filepath.Join(tmp, "calls.log")
	for name, body := range map[string]string{
		"gradle": `printf 'gradle %s\n' "$*" >>"` + log + `"; exit ` + gradleRC,
		"adb":    `printf 'adb %s socket=%s\n' "$*" "$ADB_SERVER_SOCKET" >>"` + log + `"`,
		"sleep":  `exit 0`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(tmp, "ready")
	if ready {
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + a + "'"
	}
	script := `export PATH="` + bin + `:/usr/bin:/bin"
source "$1/packages/lib/entrypoint-lib.sh"
cd "` + work + `" && proveo_android_install ` + strings.Join(quoted, " ") + `; echo "rc=$?"`
	cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t))
	cmd.Env = append(os.Environ(), "PROVEO_TOOLCHAIN_READY="+marker, "PROVEO_ANDROID_LOG="+filepath.Join(tmp, "install.log"),
		"PROVEO_ANDROID_WAIT=0", "ADB_SERVER_SOCKET=tcp:host.docker.internal:5037", "PROVEO_HOME="+tmp)
	b, _ := cmd.CombinedOutput()
	c, _ := os.ReadFile(log)
	return string(b), string(c)
}

func TestAndroidInstallBuildsInstallsAndLaunchesEachModule(t *testing.T) {
	out, calls := androidInstall(t, true, "0", ":app|ca.proveo.hello", "|ca.proveo.root")
	want := "gradle --no-daemon -q :app:installDebug\n" +
		"adb shell monkey -p ca.proveo.hello -c android.intent.category.LAUNCHER 1 socket=tcp:host.docker.internal:5037\n" +
		"gradle --no-daemon -q installDebug\n" +
		"adb shell monkey -p ca.proveo.root -c android.intent.category.LAUNCHER 1 socket=tcp:host.docker.internal:5037\n"
	if calls != want {
		t.Errorf("proveo_android_install calls =\n%s\nwant\n%s", calls, want)
	}
	if !strings.Contains(out, "rc=0") || !strings.Contains(out, "ca.proveo.hello installed and launched") {
		t.Errorf("proveo_android_install output = %q", out)
	}
}

func TestAndroidInstallSkipsTheLaunchWhenTheBuildFails(t *testing.T) {
	out, calls := androidInstall(t, true, "1", ":app|ca.proveo.hello")
	if strings.Contains(calls, "adb ") {
		t.Errorf("proveo_android_install launched after a failed build:\n%s", calls)
	}
	if !strings.Contains(out, "rc=1") || !strings.Contains(out, ":app:installDebug failed") {
		t.Errorf("proveo_android_install output = %q", out)
	}
}

func TestAndroidInstallWaitsForTheSeedsToolchain(t *testing.T) {
	out, calls := androidInstall(t, false, "0", ":app|ca.proveo.hello")
	if calls != "" {
		t.Errorf("proveo_android_install ran before the toolchain marker:\n%s", calls)
	}
	if !strings.Contains(out, "rc=1") || !strings.Contains(out, "not ready") {
		t.Errorf("proveo_android_install output = %q", out)
	}
}

func TestSeedMarksTheToolchainReadyAfterProvisioning(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "packages", "lib", "entrypoint-lib.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	seed := s[strings.Index(s, "proveo_seed() {"):]
	seed = seed[:strings.Index(seed, "\n}\n")]
	clear, provision, mark := strings.Index(seed, `rm -f "$PROVEO_TOOLCHAIN_READY"`), strings.Index(seed, "proveo_provision_toolchain"), strings.Index(seed, `: > "$PROVEO_TOOLCHAIN_READY"`)
	if clear < 0 || provision < 0 || mark < 0 || !(clear < provision && provision < mark) {
		t.Errorf("proveo_seed must clear the marker, provision, then mark (clear=%d provision=%d mark=%d)", clear, provision, mark)
	}
}
