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
// mode: ok, buildfails (gradle exits 1), noapk (gradle writes no APK), installfails (adb install exits 1).
func androidInstall(t *testing.T, ready bool, mode string, args ...string) (out, calls string) {
	t.Helper()
	bash := bashOrSkip(t)
	bin, work, tmp := t.TempDir(), t.TempDir(), t.TempDir()
	log := filepath.Join(tmp, "calls.log")
	for name, body := range map[string]string{
		"gradle": `printf 'gradle %s\n' "$*" >>"` + log + `"
case "` + mode + `" in buildfails) exit 1 ;; noapk) exit 0 ;; esac
d="${3%assembleDebug}"; d="${d#:}"; d="${d%:}"; d="$(printf '%s' "${d:-.}" | tr : /)"
mkdir -p "$d/build/outputs/apk/debug" "$d/build/outputs/apk/androidTest/debug"
: >"$d/build/outputs/apk/debug/out-debug.apk"; : >"$d/build/outputs/apk/androidTest/debug/a-debug.apk"`,
		"adb": `printf 'adb %s socket=%s\n' "$*" "$ADB_SERVER_SOCKET" >>"` + log + `"
[ "$1" = install ] && [ "` + mode + `" = installfails ] && exit 1
exit 0`,
		"sleep": `exit 0`,
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
	out, calls := androidInstall(t, true, "ok", ":app|ca.proveo.hello", "|ca.proveo.root")
	sock := " socket=tcp:host.docker.internal:5037\n"
	want := "gradle --no-daemon -q :app:assembleDebug\n" +
		"adb install -r app/build/outputs/apk/debug/out-debug.apk" + sock +
		"adb shell monkey -p ca.proveo.hello -c android.intent.category.LAUNCHER 1" + sock +
		"gradle --no-daemon -q assembleDebug\n" +
		"adb install -r ./build/outputs/apk/debug/out-debug.apk" + sock +
		"adb shell monkey -p ca.proveo.root -c android.intent.category.LAUNCHER 1" + sock
	if calls != want {
		t.Errorf("proveo_android_install calls =\n%s\nwant\n%s", calls, want)
	}
	if !strings.Contains(out, "rc=0") || !strings.Contains(out, "ca.proveo.hello installed and launched") {
		t.Errorf("proveo_android_install output = %q", out)
	}
}

func TestAndroidInstallSkipsTheInstallWhenTheBuildFails(t *testing.T) {
	out, calls := androidInstall(t, true, "buildfails", ":app|ca.proveo.hello")
	if strings.Contains(calls, "adb ") {
		t.Errorf("proveo_android_install ran adb after a failed build:\n%s", calls)
	}
	if !strings.Contains(out, "rc=1") || !strings.Contains(out, ":app:assembleDebug failed") {
		t.Errorf("proveo_android_install output = %q", out)
	}
}

func TestAndroidInstallNeedsTheDebugAPK(t *testing.T) {
	out, calls := androidInstall(t, true, "noapk", ":app|ca.proveo.hello")
	if strings.Contains(calls, "adb ") {
		t.Errorf("proveo_android_install ran adb with no APK:\n%s", calls)
	}
	if !strings.Contains(out, "rc=1") || !strings.Contains(out, "wrote no debug APK") {
		t.Errorf("proveo_android_install output = %q", out)
	}
}

func TestAndroidInstallSkipsTheLaunchWhenTheInstallFails(t *testing.T) {
	out, calls := androidInstall(t, true, "installfails", ":app|ca.proveo.hello")
	if strings.Contains(calls, "monkey") {
		t.Errorf("proveo_android_install launched after a failed install:\n%s", calls)
	}
	if !strings.Contains(out, "rc=1") || !strings.Contains(out, "adb install app/build/outputs/apk/debug/out-debug.apk failed") {
		t.Errorf("proveo_android_install output = %q", out)
	}
}

func TestAndroidInstallWaitsForTheSeedsToolchain(t *testing.T) {
	out, calls := androidInstall(t, false, "ok", ":app|ca.proveo.hello")
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
	clear, handoff := strings.Index(seed, `rm -f "$PROVEO_TOOLCHAIN_READY"`), strings.LastIndex(seed, `_proveo_seed_provision "$target"`)
	if clear < 0 || handoff < 0 || clear > handoff {
		t.Errorf("proveo_seed must clear the marker before handing off to provisioning (clear=%d handoff=%d)", clear, handoff)
	}
	prov := provisionBody(t, s)
	provision, mark := strings.Index(prov, "proveo_provision_toolchain"), strings.Index(prov, `: > "$PROVEO_TOOLCHAIN_READY"`)
	if provision < 0 || mark < 0 || provision > mark {
		t.Errorf("_proveo_seed_provision must provision, then mark (provision=%d mark=%d)", provision, mark)
	}
}
