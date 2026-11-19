//go:build image

// SPEC: _spec/_plans/isolated-browser-profiles.puml
package imagetest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/imagetest"
)

func TestImageBrowserDocker(t *testing.T) {
	const image = "chromedp/headless-shell:158.0.8127.37"
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	if err := imagetest.DaemonReady(); err != nil {
		t.Fatal(err)
	}
	arch := imagetest.Docker(imagetest.ProbeTimeout, nil, "image", "inspect", "--format", "{{.Architecture}}", image)
	if !arch.OK() {
		t.Skipf("fixture image is missing; run docker pull %s", image)
	}
	dir := t.TempDir()
	for _, build := range []struct {
		file string
		args []string
	}{
		{"browser.test", []string{"test", "-c", "-o", filepath.Join(dir, "browser.test"), "./internal/guestbrowser"}},
		{"launcher", []string{"build", "-o", filepath.Join(dir, "launcher"), "./cmd/proveo-entrypoint"}},
	} {
		goBuild := exec.CommandContext(t.Context(), "go", build.args...)
		goBuild.Dir = "../.."
		goBuild.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+strings.TrimSpace(arch.Out), "CGO_ENABLED=0")
		if out, err := goBuild.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", build.file, err, out)
		}
	}

	r := browserDockerRun(t, "--user", "1000:1000",
		"--tmpfs", "/var/lib/proveo/browser:uid=1000,gid=1000,mode=0700",
		"--mount", "type=bind,source="+filepath.Join(dir, "browser.test")+",target=/browser.test,readonly",
		"--mount", "type=bind,source="+filepath.Join(dir, "launcher")+",target=/launcher,readonly",
		"-e", "HOME=/tmp",
		"-e", "PROVEO_TEST_CHROMIUM=/headless-shell/headless-shell",
		"-e", "PROVEO_TEST_BROWSER_LAUNCHER=/launcher",
		"--entrypoint", "/browser.test", image, "-test.v", "-test.timeout=90s")
	if !r.OK() {
		t.Fatalf("Chromium and launcher tests in Docker: %v\n%s", r.Err, r.Out)
	}
	t.Log(r.Out)
}

func TestImageBrowserClients(t *testing.T) {
	for _, target := range []string{"claudecode-browser", "codex-browser", "cursor-browser", "opencode-browser", "hermes"} {
		t.Run(target, func(t *testing.T) {
			image := imagetest.Resolve(target, "proveo/"+target+":latest")
			imagetest.New(t, image)

			probe := codingBrowserProbe
			if target == "hermes" {
				probe = hermesBrowserProbe
			}
			r := browserDockerRun(t, "--user", "1000:1000", "--entrypoint", "bash", image, "-c", browserLauncherProbe+probe)
			if !r.OK() {
				t.Fatalf("%s browser client: %v\n%s", target, r.Err, r.Out)
			}
		})
	}
}

func browserDockerRun(t *testing.T, args ...string) imagetest.Result {
	t.Helper()
	dir := t.TempDir()
	name := "proveo-browser-test-" + filepath.Base(dir) + "-" + time.Now().Format("150405.000000000")
	t.Cleanup(func() { imagetest.Docker(imagetest.ProbeTimeout, nil, "rm", "-f", name) })
	return imagetest.Docker(3*time.Minute, nil, append([]string{"run", "--rm", "--init", "--name", name, "--network", "none"}, args...)...)
}

const browserLauncherProbe = `
set -euo pipefail
proveo-entrypoint browser check
tmp=$(mktemp -d)
mkfifo "$tmp/control"
exec 3<>"$tmp/control"
proveo-entrypoint browser fresh 3>&- <"$tmp/control" &
launcher=$!
trap 'exec 3>&-; wait "$launcher"' EXIT
printf 'browser-test\n' >&3
viewport() {
  curl --noproxy 127.0.0.1 --max-time 15 -fsS -H 'X-Proveo-Viewport: browser-test' "http://127.0.0.1:9222/$1"
}
for _ in {1..60}; do
  if viewport health 2>/dev/null; then break; fi
  kill -0 "$launcher"
  sleep 0.25
done
viewport health
`

const codingBrowserProbe = `
(
  exec 3>&-
  export HOME="$tmp" AGENT_BROWSER_SOCKET_DIR="$tmp"
  if PROVEO_BROWSER_MODE=none agent-browser open about:blank >"$tmp/off" 2>&1; then exit 1; fi
  grep -F 'Browser integration is off' "$tmp/off"
  export PROVEO_BROWSER_MODE=isolated
  agent-browser open 'data:text/html,<title>proveo-browser-client</title>'
  agent-browser close
)
viewport tabs | grep -F proveo-browser-client
`

const hermesBrowserProbe = `
(
  exec 3>&-
  export PROVEO_SEED_ONLY=1 PROVEO_SEED_PHASE=none
  PROVEO_BROWSER_MODE=isolated /entrypoint.sh --prepare
  cd /opt/hermes
  HERMES_MANAGED_DIR=/var/lib/proveo/browser/hermes-isolated /opt/hermes/.venv/bin/python - <<'PY'
import json
from hermes_cli.env_loader import load_hermes_dotenv
from tools.browser_use_cli import browser_exec

load_hermes_dotenv()
response = json.loads(browser_exec('new_tab("data:text/html,<title>proveo-browser-client</title>"); print(page_info())'))
assert response["success"], response
PY
  PROVEO_BROWSER_MODE=none /entrypoint.sh --prepare
  HERMES_MANAGED_DIR=/var/lib/proveo/browser/hermes-none /opt/hermes/.venv/bin/python - <<'PY'
from hermes_cli.config import load_config
assert "browser" in load_config()["agent"]["disabled_toolsets"]
PY
)
viewport tabs | grep -F proveo-browser-client
`
