#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/docker-build.sh
source "$SCRIPT_DIR/../lib/docker-build.sh"

IMAGE_NAME="$(proveo_test_image "${PROVEO_CECLI_IMAGE:-proveo/cecli:latest}")"

echo "── Runtime user checks ──────────────────────────────"
docker run --rm --entrypoint bash "$IMAGE_NAME" -c \
  '[ "$(id -u)" != "0" ] && [ "$(whoami)" = "cecli" ] && ! command -v gosu >/dev/null'
docker run --rm --user 4242:4242 --entrypoint bash "$IMAGE_NAME" -c \
  'source /entrypoint-lib.sh && ensure_runtime_user && [ "$(id -u)" = "4242" ] && [ -w "$HOME" ]'
echo "✅ non-root default user, no gosu, arbitrary --user uid usable"

docker run --rm --entrypoint bash "$IMAGE_NAME" -c 'git --version && gh --version'
docker run --rm --user 4242:4242 --entrypoint bash \
  -e GIT_AUTHOR_NAME="Proveo Dev" -e GIT_AUTHOR_EMAIL="dev@proveo.test" "$IMAGE_NAME" -c '
    source /entrypoint-lib.sh && ensure_runtime_user && bridge_git_identity \
      && [ "$(git config --get user.name)" = "Proveo Dev" ] \
      && [ "$(git config --get user.email)" = "dev@proveo.test" ]'
echo "✅ git + gh baked in, env git identity resolves via git config"

# SPEC: _spec/_devops/agent-version-pin.puml
echo "── Agent version pin ────────────────────────────────"
AGENT_VERSION_LABEL="$(docker image inspect -f '{{index .Config.Labels "proveo.agent.version"}}' "$IMAGE_NAME")"
[ -n "$AGENT_VERSION_LABEL" ] || { echo "❌ proveo.agent.version label missing (image predates the pin — proveo build cecli)" >&2; exit 1; }
[ "$(docker image inspect -f '{{index .Config.Labels "proveo.agent"}}' "$IMAGE_NAME")" = "cecli-dev" ] \
  || { echo "❌ proveo.agent label does not name cecli-dev" >&2; exit 1; }
docker run --rm --entrypoint bash "$IMAGE_NAME" -c \
  "/opt/cecli/bin/pip show cecli-dev | grep -qx 'Version: $AGENT_VERSION_LABEL'"
echo "✅ cecli-dev==$AGENT_VERSION_LABEL pinned, installed and labeled"

# SPEC: _spec/defs/agent-definition-sharing.puml
echo "── Seeded subagents ─────────────────────────────────"
docker run --rm "$IMAGE_NAME" bash -c 'set -euo pipefail
python3 --version && cecli --version
test -d "$CECLI_HOME/agents" || { echo "❌ entrypoint did not seed $CECLI_HOME/agents" >&2; exit 1; }
python3 - "$CECLI_HOME/agents" <<"PY"
import json, os, sys
from cecli.helpers.agents.service import AgentService
agents_dir = sys.argv[1]
roster = set(json.load(open("/opt/proveo/subagents/_roster.json"))["cecli"])
seeded = {f[:-3] for f in os.listdir(agents_dir) if f.endswith(".md")}
assert seeded == roster, f"seeded {sorted(seeded)} != roster {sorted(roster)}"
AgentService._global_registry = {}
AgentService.build_registry([agents_dir])
registry = AgentService.get_registry()
missing = roster - set(registry)
assert not missing, f"registry did not load seeded subagents: {sorted(missing)}"
for name in sorted(roster):
    assert registry[name].prompt, f"{name}: empty prompt"
    assert registry[name].metadata.get("description"), f"{name}: no description"
print("subagents:", " ".join(sorted(roster)), "| cecli built-ins:", " ".join(sorted(set(registry) - roster)))
PY'
echo "✅ entrypoint seeds the cecli roster from /opt/proveo/subagents and cecli's registry loads it"
