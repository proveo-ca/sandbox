//go:build image

// SPEC: _spec/tests/testing-strategy.puml, _spec/_plans/host-shell-to-go.puml
package imagetest_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/imagetest"
)

// SPEC: _spec/packages/lib/seed-and-launch.puml, _spec/_devops/agent-version-pin.puml, _spec/defs/agent-definition-sharing.puml
func TestImageCecli(t *testing.T) {
	image := imagetest.Resolve("PROVEO_CECLI_IMAGE", "proveo/cecli:latest")
	s := imagetest.New(t, image)

	t.Log("── Runtime user checks ──────────────────────────────")
	cecliAll(s, "non-root default user, no gosu, arbitrary --user uid usable",
		[]string{"run", "--rm", "--entrypoint", "bash", image, "-c",
			`[ "$(id -u)" != "0" ] && [ "$(whoami)" = "cecli" ] && ! command -v gosu >/dev/null`},
		[]string{"run", "--rm", "--user", "4242:4242", "--entrypoint", "bash", image, "-c",
			`source /entrypoint-lib.sh && ensure_runtime_user && [ "$(id -u)" = "4242" ] && [ -w "$HOME" ]`},
	)

	cecliAll(s, "ships the Kit's startup command (/usr/local/bin/proveo-seed)",
		[]string{"run", "--rm", "--entrypoint", "bash", image, "-c", `test -x /usr/local/bin/proveo-seed`},
	)

	cecliAll(s, "git + gh + ffmpeg baked in, env git identity resolves via git config",
		[]string{"run", "--rm", "--entrypoint", "bash", image, "-c", `git --version && gh --version && ffmpeg -hide_banner -version`},
		[]string{"run", "--rm", "--user", "4242:4242", "--entrypoint", "bash",
			"-e", "GIT_AUTHOR_NAME=Proveo Dev", "-e", "GIT_AUTHOR_EMAIL=dev@proveo.test", image, "-c", `
    source /entrypoint-lib.sh && ensure_runtime_user && bridge_git_identity \
      && [ "$(git config --get user.name)" = "Proveo Dev" ] \
      && [ "$(git config --get user.email)" = "dev@proveo.test" ]`},
	)

	t.Log("── Agent version pin ────────────────────────────────")
	version := cecliLabel(image, "proveo.agent.version")
	s.Check(fmt.Sprintf("cecli-dev==%s pinned, installed and labeled", version), func(t *testing.T) {
		if version == "" {
			t.Fatal("proveo.agent.version label missing (image predates the pin — proveo build cecli)")
		}
		if cecliLabel(image, "proveo.agent") != "cecli-dev" {
			t.Fatal("proveo.agent label does not name cecli-dev")
		}
		cecliRun(t, "run", "--rm", "--entrypoint", "bash", image, "-c",
			fmt.Sprintf("/opt/cecli/bin/pip show cecli-dev | grep -qx 'Version: %s'", version))
	})

	t.Log("── Seeded subagents ─────────────────────────────────")
	cecliAll(s, "entrypoint seeds the cecli roster from /opt/proveo/subagents and cecli's registry loads it",
		[]string{"run", "--rm", image, "bash", "-c", cecliSubagentsCmd},
	)
}

// cecliAll passes desc when every docker argv exits zero, in order.
func cecliAll(s *imagetest.Suite, desc string, runs ...[]string) {
	s.Check(desc, func(t *testing.T) {
		for _, args := range runs {
			cecliRun(t, args...)
		}
	})
}

func cecliRun(t *testing.T, args ...string) {
	t.Helper()
	r := imagetest.Docker(imagetest.DefaultTimeout, nil, args...)
	t.Log(strings.TrimSpace(r.Out))
	if !r.OK() {
		t.Fatalf("docker %s: %v", strings.Join(args, " "), r.Err)
	}
}

func cecliLabel(image, key string) string {
	r := imagetest.Docker(imagetest.DefaultTimeout, nil, "image", "inspect", "-f",
		fmt.Sprintf(`{{index .Config.Labels %q}}`, key), image)
	if !r.OK() {
		return ""
	}
	return strings.TrimSpace(r.Out)
}

const cecliSubagentsCmd = `set -euo pipefail
python3 --version && cecli --version
test -d "$CECLI_HOME/agents" || { echo "FAIL: entrypoint did not seed $CECLI_HOME/agents" >&2; exit 1; }
python3 - "$CECLI_HOME/agents" <<"PY"
import json, os, sys
from cecli.helpers.agents.service import AgentService
agents_dir = sys.argv[1]
roster = set(json.load(open("/opt/proveo/subagents/_roster.json"))["cecli"])
# The FILES are ours: exactly the roster, nothing missing, nothing stray.
seeded = {f[:-3] for f in os.listdir(agents_dir) if f.endswith(".md")}
assert seeded == roster, f"seeded {sorted(seeded)} != roster {sorted(roster)}"
# The REGISTRY belongs to cecli: it merges its own built-ins, memorizer and
# worker among them, with ours — so it must CONTAIN the roster, not equal it.
AgentService._global_registry = {}
AgentService.build_registry([agents_dir])
registry = AgentService.get_registry()
missing = roster - set(registry)
assert not missing, f"registry did not load seeded subagents: {sorted(missing)}"
for name in sorted(roster):
    assert registry[name].prompt, f"{name}: empty prompt"
    assert registry[name].metadata.get("description"), f"{name}: no description"
print("subagents:", " ".join(sorted(roster)), "| cecli built-ins:", " ".join(sorted(set(registry) - roster)))
PY`
