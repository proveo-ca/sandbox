#!/usr/bin/env bash
# SPEC: _spec/defs/claudecode/chrome-bridge.puml
# tests/test_chrome_bridge.sh - the container half of the Claude in Chrome bridge.
# A fake host relay inside the container stands in for `proveo run`'s, so the
# chain is driven end to end without a browser.

read -r -d '' CHAIN_SCRIPT <<'EOS'
set -e
export HOME=/tmp
node -e '
  const net = require("net"), fs = require("fs");
  const srv = net.createServer(c => {
    let buf = "";
    c.on("data", d => {
      buf += d;
      const lines = buf.split("\n");
      if (lines.length >= 3) c.end("HS=" + lines[0] + ";PAYLOAD=" + lines[1] + "\n");
    });
  });
  srv.listen(0, "127.0.0.1", () => fs.writeFileSync("/tmp/fake-host.port", String(srv.address().port)));
  setTimeout(() => process.exit(0), 20000);
' &
for _ in $(seq 1 50); do [ -s /tmp/fake-host.port ] && break; sleep 0.1; done
export PROVEO_CHROME_BRIDGE="127.0.0.1:$(cat /tmp/fake-host.port)" PROVEO_CHROME_BRIDGE_TOKEN=tok-123
source /entrypoint-lib.sh
proveo_chrome_bridge claudecode >/dev/null
sock="$(head -n1 /tmp/proveo-chrome-bridge.sock-path)"
perms="$(stat -c '%a' "$(dirname "$sock")") $(stat -c '%a' "$sock")"
reply="$(node -e '
  const net = require("net");
  const c = net.connect(process.argv[1], () => c.write("hello-from-claude\n\n"));
  c.on("data", d => { process.stdout.write(d.toString()); c.end(); });
' "$sock")"
echo "CHAIN ready=${PROVEO_CHROME_READY:-} perms=${perms} dir=$(dirname "$sock") reply=${reply}"
EOS

for image in $(images_to_test); do
  tag=$(image_tag "$image")
  assert_success "[$tag] chrome-bridge.js is baked and parses" "$image" \
    "test -f /opt/proveo/lib/chrome-bridge.js && node --check /opt/proveo/lib/chrome-bridge.js"
  assert_output_contains "[$tag] proveo_chrome_bridge is a no-op without PROVEO_CHROME_BRIDGE" "$image" \
    'source /entrypoint-lib.sh; unset PROVEO_CHROME_BRIDGE; proveo_chrome_bridge claudecode; echo "ready=[${PROVEO_CHROME_READY:-}]"' \
    "ready=[]"
  assert_output_contains "[$tag] the relay refuses to run without a token" "$image" \
    'source /entrypoint-lib.sh; export PROVEO_CHROME_BRIDGE=127.0.0.1:9; unset PROVEO_CHROME_BRIDGE_TOKEN; proveo_chrome_bridge claudecode 2>&1; echo "ready=[${PROVEO_CHROME_READY:-}]"' \
    "ready=[]"
  assert_output_contains "[$tag] the relay listens where Claude Code looks, 0700/0600, and carries handshake + payload" "$image" \
    "$CHAIN_SCRIPT" \
    "CHAIN ready=1 perms=700 600 dir=/tmp/claude-mcp-browser-bridge-"
  assert_output_contains "[$tag] the host end sees the token line first, then Claude Code's bytes" "$image" \
    "$CHAIN_SCRIPT" \
    "reply=HS=PROVEO-CHROME-BRIDGE tok-123;PAYLOAD=hello-from-claude"
done
