// SPEC: _spec/internal/cdn/distribution-update.puml
package installtest_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// cdnEnv names a pre-staged CDN dir (e.g. apps/cli/public/cli after `proveo-dev stage-cdn`).
const cdnEnv = "PROVEO_INSTALLTEST_CDN"

const stagedVersion = "0.0.0-installtest"

func TestInstallScripts(t *testing.T) {
	root := repoRoot(t)
	cliRoot := filepath.Join(root, "apps", "cli", "public", "cli")
	installScript := filepath.Join(cliRoot, "install.sh")
	uninstallScript := filepath.Join(cliRoot, "uninstall.sh")
	for _, s := range []string{installScript, uninstallScript} {
		if _, err := os.ReadFile(s); err != nil {
			t.Fatal(err)
		}
	}
	bash := interpreter(t)
	asset := platformAsset(t)
	t.Logf("interpreter: %s", bash)

	cdn := os.Getenv(cdnEnv)
	switch {
	case cdn == "":
		cdn = stageCDN(t, root, asset)
	case !filepath.IsAbs(cdn):
		cdn = filepath.Join(root, cdn)
	}
	t.Logf("cdn: %s", cdn)

	tmp := t.TempDir()
	path := os.Getenv("PATH")

	mustSucceed(t, "install script has valid Bash syntax", nil, bash, "-n", installScript)
	mustSucceed(t, "uninstall script has valid Bash syntax", nil, bash, "-n", uninstallScript)
	fileExists(t, "CDN checksums.txt present", filepath.Join(cdn, "checksums.txt"))
	fileExists(t, "CDN latest.json present", filepath.Join(cdn, "latest.json"))
	fileExists(t, fmt.Sprintf("platform binary staged (%s)", asset), filepath.Join(cdn, "bin", asset))

	version := channelVersion(filepath.Join(cdn, "latest.json"))

	installHome := filepath.Join(tmp, "home")
	installRoot := filepath.Join(tmp, "install-root")
	fakeBin := filepath.Join(tmp, "install-bin")
	mkdirs(t, installHome, fakeBin)
	writeExec(t, filepath.Join(fakeBin, "curl"), fakeCurl)

	install := func(home, installRoot, base string, skipInit bool) []string {
		env := []string{
			"HOME=" + home,
			"SHELL=/bin/bash",
			"PATH=" + fakeBin + ":" + path,
			"PROVEO_INSTALL_ROOT=" + installRoot,
			"PROVEO_ASSET_BASE_URL=file://" + base,
			"PROVEO_CLI_BASE_URL=file://" + base,
		}
		if skipInit {
			env = append(env, "PROVEO_SKIP_INIT=1")
		}
		return env
	}

	outputContains(t, "install.sh installs Go proveo", "proveo v"+version+" installed to:",
		install(installHome, installRoot, cdn, true), bash, installScript)

	installed := filepath.Join(installRoot, "bin", "proveo")
	fileExists(t, "install writes proveo binary", installed)
	fileExecutable(t, "installed proveo is executable", installed)
	fileExists(t, "install writes uninstall.sh", filepath.Join(installRoot, "uninstall.sh"))
	noPath(t, "install does not ship bash lib/", filepath.Join(installRoot, "lib"))
	noPath(t, "install does not ship help.sh", filepath.Join(installRoot, "bin", "help.sh"))

	binPath := []string{"PATH=" + filepath.Join(installRoot, "bin") + ":" + path}
	outputContains(t, "installed proveo --version works", "proveo version", binPath, installed, "--version")
	outputContains(t, "installed proveo version alias works", "proveo version", binPath, installed, "version")

	badCDN := filepath.Join(tmp, "bad-cdn")
	mkdirs(t, filepath.Join(badCDN, "bin"))
	copyFile(t, filepath.Join(cdn, "bin", asset), filepath.Join(badCDN, "bin", asset))
	copyFile(t, uninstallScript, filepath.Join(badCDN, "uninstall.sh"))
	copyFile(t, filepath.Join(cdn, "latest.json"), filepath.Join(badCDN, "latest.json"))
	writeFile(t, filepath.Join(badCDN, "checksums.txt"), strings.Repeat("0", 64)+"  "+asset+"\n")
	mustFail(t, "install rejects checksum mismatch",
		install(filepath.Join(tmp, "home-bad"), filepath.Join(tmp, "install-bad"), badCDN, true), bash, installScript)

	stubCDN := filepath.Join(tmp, "stub-cdn")
	stubLog := filepath.Join(tmp, "stub-argv.log")
	stageStubCDN(t, stubCDN, asset, cdn, uninstallScript, stubProveo(stubLog, "stub proveo init reached", 0))
	outputContains(t, "install.sh runs the sbx bootstrap after placing the binary", "Setting up the sbx backend",
		install(filepath.Join(tmp, "home-init"), filepath.Join(tmp, "install-init"), stubCDN, false), bash, installScript)
	fileContains(t, "the bootstrap invokes `proveo init`", stubLog, "init")

	skipLog := filepath.Join(tmp, "stub-argv-skip.log")
	stageStubCDN(t, stubCDN, asset, cdn, uninstallScript, stubProveo(skipLog, "stub proveo init reached", 0))
	outputContains(t, "PROVEO_SKIP_INIT opts out of the bootstrap", "Skipping the sbx bootstrap",
		install(filepath.Join(tmp, "home-skip"), filepath.Join(tmp, "install-skip"), stubCDN, true), bash, installScript)
	fileLacks(t, "the skipped bootstrap never calls init", skipLog, "init")

	failingCDN := filepath.Join(tmp, "failing-cdn")
	failingLog := filepath.Join(tmp, "stub-argv-fail.log")
	stageStubCDN(t, failingCDN, asset, cdn, uninstallScript, stubProveo(failingLog, "host not ready to run: KVM device", 1))
	outputContains(t, "a host that cannot run sbx yet does not fail the install", "host not ready to run",
		install(filepath.Join(tmp, "home-notready"), filepath.Join(tmp, "install-notready"), failingCDN, false), bash, installScript)
	fileContains(t, "the not-ready case reached init", failingLog, "init")
	outputContains(t, "and says how to finish once the host is fixed", "proveo itself is installed",
		install(filepath.Join(tmp, "home-notready2"), filepath.Join(tmp, "install-notready2"), failingCDN, false), bash, installScript)

	mustSucceed(t, "uninstall.sh removes install root", []string{
		"HOME=" + installHome,
		"PROVEO_INSTALL_ROOT=" + installRoot,
		"PROVEO_UNINSTALL_ASSUME_YES=1",
	}, bash, uninstallScript)
	noPath(t, "uninstall removes install root", installRoot)
}

const fakeCurl = `#!/bin/sh
src=""
dest=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) dest="$2"; shift 2 ;;
    -*) shift ;;
    *) src="$1"; shift ;;
  esac
done
[ -n "$src" ] && [ -n "$dest" ] || exit 1
src="${src#file://}"
exec cp "$src" "$dest"
`

const stubTemplate = `#!/bin/sh
printf '%%s\n' "$*" >> %s
case "$1" in
  setup) exit 0 ;;
  init)  echo %s; exit %d ;;
esac
exit 0
`

func stubProveo(log, initLine string, initExit int) string {
	return fmt.Sprintf(stubTemplate, shQuote(log), shQuote(initLine), initExit)
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func interpreter(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat("/bin/bash"); err != nil {
			t.Skipf("/bin/bash absent: %v", err)
		}
		return "/bin/bash"
	}
	p, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not on PATH: %v", err)
	}
	return p
}

func platformAsset(t *testing.T) string {
	t.Helper()
	switch runtime.GOOS {
	case "linux", "darwin":
	default:
		t.Skipf("install.sh suite covers linux and darwin, not %s", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64", "arm64":
	default:
		t.Skipf("install.sh suite covers amd64 and arm64, not %s", runtime.GOARCH)
	}
	return "proveo-" + runtime.GOOS + "-" + runtime.GOARCH
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test dir")
		}
		dir = parent
	}
}

// stageCDN builds the host asset from ./cmd/proveo and writes the CDN tree install.sh reads.
func stageCDN(t *testing.T, root, asset string) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go toolchain required to stage the CDN: %v", err)
	}
	cdn := filepath.Join(t.TempDir(), "cdn")
	bin := filepath.Join(cdn, "bin", asset)
	mkdirs(t, filepath.Dir(bin))
	cmd := exec.CommandContext(t.Context(), goBin, "build", "-trimpath",
		"-ldflags=-s -w -X main.version="+stagedVersion, "-o", bin, "./cmd/proveo")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/proveo: %v\n%s", err, out)
	}
	sum := sha256File(t, bin)
	writeFile(t, filepath.Join(cdn, "checksums.txt"), sum+"  "+asset+"\n")
	latest, err := json.MarshalIndent(map[string]any{
		"version":   stagedVersion,
		"checksums": map[string]string{asset: sum},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cdn, "latest.json"), string(latest)+"\n")
	copyFile(t, filepath.Join(root, "apps", "cli", "public", "cli", "uninstall.sh"), filepath.Join(cdn, "uninstall.sh"))
	return cdn
}

// stageStubCDN serves a stub proveo that appends its argv to a log, so `proveo init` never reaches GitHub.
func stageStubCDN(t *testing.T, dir, asset, cdn, uninstall, script string) {
	t.Helper()
	bin := filepath.Join(dir, "bin", asset)
	mkdirs(t, filepath.Dir(bin))
	writeExec(t, bin, script)
	copyFile(t, filepath.Join(cdn, "latest.json"), filepath.Join(dir, "latest.json"))
	copyFile(t, uninstall, filepath.Join(dir, "uninstall.sh"))
	writeFile(t, filepath.Join(dir, "checksums.txt"), sha256File(t, bin)+"  "+asset+"\n")
}

func channelVersion(latest string) string {
	b, err := os.ReadFile(latest)
	if err != nil {
		return "dev"
	}
	var m struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &m) != nil || m.Version == "" {
		return "dev"
	}
	return m.Version
}

// hermeticEnv drops inherited PROVEO_* so an operator's shell cannot steer the installer.
func hermeticEnv(overrides []string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PROVEO_") {
			env = append(env, kv)
		}
	}
	return append(env, overrides...)
}

func run(t *testing.T, env []string, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Env = hermeticEnv(env)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func clip(s string) string {
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

func mustSucceed(t *testing.T, desc string, env []string, name string, args ...string) {
	t.Helper()
	t.Run(desc, func(t *testing.T) {
		if out, err := run(t, env, name, args...); err != nil {
			t.Errorf("%v\nOutput: %s", err, clip(out))
		}
	})
}

func mustFail(t *testing.T, desc string, env []string, name string, args ...string) {
	t.Helper()
	t.Run(desc, func(t *testing.T) {
		if out, err := run(t, env, name, args...); err == nil {
			t.Errorf("exited 0, want non-zero\nOutput: %s", clip(out))
		}
	})
}

func outputContains(t *testing.T, desc, expected string, env []string, name string, args ...string) {
	t.Helper()
	t.Run(desc, func(t *testing.T) {
		out, err := run(t, env, name, args...)
		if err != nil || !strings.Contains(out, expected) {
			t.Errorf("err=%v\nOutput: %s\nExpected to contain: %s", err, clip(out), expected)
		}
	})
}

func fileExists(t *testing.T, desc, file string) {
	t.Helper()
	t.Run(desc, func(t *testing.T) {
		if fi, err := os.Stat(file); err != nil || !fi.Mode().IsRegular() {
			t.Errorf("Missing file: %s", file)
		}
	})
}

func fileExecutable(t *testing.T, desc, file string) {
	t.Helper()
	t.Run(desc, func(t *testing.T) {
		if fi, err := os.Stat(file); err != nil || fi.Mode().Perm()&0o111 == 0 {
			t.Errorf("File is not executable: %s", file)
		}
	})
}

func noPath(t *testing.T, desc, path string) {
	t.Helper()
	t.Run(desc, func(t *testing.T) {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Path still exists: %s", path)
		}
	})
}

func fileContains(t *testing.T, desc, file, expected string) {
	t.Helper()
	t.Run(desc, func(t *testing.T) {
		b, err := os.ReadFile(file)
		if err != nil || !strings.Contains(string(b), expected) {
			t.Errorf("Expected %s to contain: %s\nOutput: %s (err=%v)", file, expected, clip(string(b)), err)
		}
	})
}

func fileLacks(t *testing.T, desc, file, unexpected string) {
	t.Helper()
	t.Run(desc, func(t *testing.T) {
		b, err := os.ReadFile(file)
		if err == nil && strings.Contains(string(b), unexpected) {
			t.Errorf("Expected %s NOT to contain: %s\nOutput: %s", file, unexpected, clip(string(b)))
		}
	})
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExec(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, fi.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
