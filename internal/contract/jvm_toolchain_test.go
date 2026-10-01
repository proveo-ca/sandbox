// SPEC: _spec/internal/sbx/host-android-adb.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// jvmRow runs ensure_jvm_toolchain on a workspace holding files, with mise, java,
// uname and sdkmanager stubbed; it returns stdout and the stubs' call log.
func jvmRow(t *testing.T, files map[string]string, java, arch string, env ...string) (string, string) {
	out, calls, _ := jvmRowHome(t, files, java, arch, env...)
	return out, calls
}

// jvmRowHome is jvmRow that also returns the agent home the row wrote into.
func jvmRowHome(t *testing.T, files map[string]string, java, arch string, env ...string) (string, string, string) {
	t.Helper()
	bash := bashOrSkip(t)
	bin, ws, sdk, home := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	log := filepath.Join(t.TempDir(), "calls.log")
	for name, body := range files {
		p := filepath.Join(ws, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub := map[string]string{
		"mise":       `echo "mise $*" >>"` + log + `"; [ "$1" = where ] && echo "` + sdk + `"; exit 0`,
		"uname":      `echo ` + arch,
		"sdkmanager": `head -c 16 >/dev/null; echo "sdkmanager $*" >>"` + log + `"`,
		"java":       `echo 'openjdk version "` + java + `" 2026-07-21' >&2`,
	}
	wrapper := filepath.Join(t.TempDir(), "aapt2")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !slicesContains(env, "NO_QEMU=1") {
		stub["qemu-x86_64"] = "exit 0"
	}
	for name, body := range stub {
		if java == "" && name == "java" {
			continue
		}
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := `export PATH="` + bin + `:/usr/bin:/bin"
source "$1/packages/lib/entrypoint-lib.sh"
cd "$2" && ensure_jvm_toolchain . && echo "ANDROID_HOME=${ANDROID_HOME:-}"`
	cmd := exec.Command(bash, "-c", script, "bash", repoRoot(t), ws)
	cmd.Env = append(append(os.Environ(), "PROVEO_HOME=", "HOME="+home, "PROVEO_HOST_OS=", "PROVEO_AAPT2_WRAPPER="+wrapper), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ensure_jvm_toolchain: %v\n%s", err, out)
	}
	b, _ := os.ReadFile(log)
	return string(out), string(b), home
}

func slicesContains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

const androidBuild = `plugins { id("com.android.application") version "9.4.1" apply false }`

func TestJVMRowIsInertWithoutGradleFiles(t *testing.T) {
	out, calls := jvmRow(t, map[string]string{"main.go": "package main"}, "25.0.4", "x86_64")
	if calls != "" || strings.Contains(out, "Detected") {
		t.Errorf("no Gradle build must provision nothing: out=%q calls=%q", out, calls)
	}
}

func TestJVMRowInstallsGradleWhenNoWrapper(t *testing.T) {
	_, calls := jvmRow(t, map[string]string{"build.gradle.kts": `plugins { java }`}, "25.0.4", "x86_64")
	if !strings.Contains(calls, "mise use -g gradle@latest") || strings.Contains(calls, "java@") || strings.Contains(calls, "android-sdk") {
		t.Errorf("plain Gradle build with JDK 25 ⇒ %q; want gradle only", calls)
	}
}

func TestJVMRowTrustsTheWrapper(t *testing.T) {
	_, calls := jvmRow(t, map[string]string{"build.gradle": "apply plugin: 'java'", "gradlew": "#!/bin/sh\n"}, "25.0.4", "x86_64")
	if calls != "" {
		t.Errorf("a gradlew build ⇒ %q; want nothing installed", calls)
	}
}

func TestJVMRowInstallsJavaBelowSeventeen(t *testing.T) {
	_, calls := jvmRow(t, map[string]string{"build.gradle": "", "gradlew": "#!/bin/sh\n"}, "11.0.2", "x86_64")
	if !strings.Contains(calls, "mise use -g java@temurin-21") {
		t.Errorf("JDK 11 ⇒ %q; want java@temurin-21", calls)
	}
}

func TestJVMRowProvisionsTheAndroidSDKOnAmd64(t *testing.T) {
	out, calls := jvmRow(t, map[string]string{"build.gradle.kts": androidBuild, "app/build.gradle.kts": `plugins { id("com.android.application") }`}, "25.0.4", "x86_64")
	if !strings.Contains(calls, "mise use -g android-sdk@20.0") || !strings.Contains(calls, "sdkmanager --licenses") {
		t.Errorf("android on amd64 ⇒ %q; want android-sdk@20.0 and accepted licenses", calls)
	}
	if !strings.Contains(out, "ANDROID_HOME=/") {
		t.Errorf("ANDROID_HOME not exported:\n%s", out)
	}
}

func TestJVMRowSkipsTheAndroidSDKOnArm64LinuxHosts(t *testing.T) {
	out, calls := jvmRow(t, map[string]string{"build.gradle.kts": androidBuild}, "25.0.4", "aarch64", "PROVEO_HOST_OS=linux")
	if strings.Contains(calls, "android-sdk") || !strings.Contains(out, "macOS hosts alone") {
		t.Errorf("android, arm64 guest, linux host ⇒ out=%q calls=%q; want the notice and no SDK", out, calls)
	}
}

func TestJVMRowEmulatesAapt2OnArm64GuestsOfMacOSHosts(t *testing.T) {
	out, calls, home := jvmRowHome(t, map[string]string{"build.gradle.kts": androidBuild}, "25.0.4", "aarch64", "PROVEO_HOST_OS=darwin")
	if !strings.Contains(calls, "mise use -g android-sdk@20.0") || !strings.Contains(calls, "sdkmanager build-tools;36.0.0") {
		t.Errorf("macOS host ⇒ calls=%q; want the SDK and build-tools 36.0.0", calls)
	}
	props, err := os.ReadFile(filepath.Join(home, ".gradle", "gradle.properties"))
	if err != nil || !strings.Contains(string(props), "android.aapt2FromMavenOverride=") || !strings.Contains(string(props), "proveo android") {
		t.Errorf("gradle.properties lacks the marked aapt2 override: %v\n%s", err, props)
	}
	if !strings.Contains(out, "under qemu-user") {
		t.Errorf("no notice that aapt2 runs emulated:\n%s", out)
	}
}

func TestJVMRowKeepsTheOperatorsGradleProperties(t *testing.T) {
	_, _, home := jvmRowHome(t, map[string]string{"build.gradle.kts": androidBuild}, "25.0.4", "aarch64", "PROVEO_HOST_OS=darwin")
	path := filepath.Join(home, ".gradle", "gradle.properties")
	first, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append([]byte("org.gradle.caching=true\n"), first...), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bashOrSkip(t), "-c", `source "$1/packages/lib/entrypoint-lib.sh"; printf 'android.aapt2FromMavenOverride=/x\n' | _proveo_write_block "$2" "$PROVEO_ANDROID_START" "$PROVEO_ANDROID_END"`, "bash", repoRoot(t), path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rewrite: %v\n%s", err, out)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "org.gradle.caching=true") || strings.Count(string(got), "aapt2FromMavenOverride") != 1 {
		t.Errorf("rewrite lost the operator's line or duplicated the block:\n%s", got)
	}
}

func TestJVMRowNeedsTheQemuFloorOnMacOSHosts(t *testing.T) {
	out, calls := jvmRow(t, map[string]string{"build.gradle.kts": androidBuild}, "25.0.4", "aarch64", "PROVEO_HOST_OS=darwin", "NO_QEMU=1")
	if strings.Contains(calls, "android-sdk") || !strings.Contains(out, "lacks the qemu aapt2 floor") {
		t.Errorf("macOS host without qemu ⇒ out=%q calls=%q", out, calls)
	}
}
