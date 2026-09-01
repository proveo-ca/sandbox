#!/usr/bin/env node
// SPEC: _spec/defs/claudecode/chrome-bridge.puml
//
// Container half of the Claude in Chrome bridge.
//
// Claude Code's claude-in-chrome MCP server does not talk to the browser; it
// connects to a Unix socket that Chrome's native messaging host — spawned by the
// Claude in Chrome extension, on the machine Chrome runs on — listens on:
//
//     /tmp/claude-mcp-browser-bridge-<username>/<pid>.sock
//
// Inside a sandbox there is no Chrome and no native host, so nothing listens and
// `--chrome` reports "Browser extension is not connected". This relay stands in
// for the native host: it listens where Claude Code looks, and pipes every
// connection to the proveo host relay (PROVEO_CHROME_BRIDGE=host:port), which
// pipes on to the real native host socket on the operator's machine. Claude Code
// on both ends is unchanged — it is the same length-prefixed byte stream, carried.
//
// Two things Claude Code checks before it will use the socket, both mirrored here:
//   - the directory must be 0700 and owned by the running uid, the socket 0600
//     ("Insecure socket directory permissions … Directory may have been tampered
//     with" is the message when they are not);
//   - <username> is os.userInfo().username, falling back to $USER, $USERNAME,
//     then "default" — an arbitrary run-as uid with no passwd entry lands on the
//     fallback, so this file computes the name with the same rule rather than
//     guessing it from the shell.
//
// The first bytes on each host connection are a handshake line carrying a
// per-run token (PROVEO_CHROME_BRIDGE_TOKEN). The host relay is a TCP listener on
// the operator's machine; the token is what keeps a stray LAN client from driving
// their browser through it.
"use strict";

const fs = require("fs");
const net = require("net");
const os = require("os");
const path = require("path");

const HANDSHAKE_PREFIX = "PROVEO-CHROME-BRIDGE ";
const SOCKET_DIR_PREFIX = "/tmp/claude-mcp-browser-bridge-";

function bridgeUsername() {
  try {
    const u = os.userInfo().username;
    if (u) return u;
  } catch (_) {
    // no passwd entry for this uid: fall through, exactly as Claude Code does
  }
  return process.env.USER || process.env.USERNAME || "default";
}

function socketDir() {
  return SOCKET_DIR_PREFIX + bridgeUsername();
}

function parseTarget(spec) {
  const i = spec.lastIndexOf(":");
  if (i <= 0) throw new Error(`PROVEO_CHROME_BRIDGE must be host:port, got ${JSON.stringify(spec)}`);
  const port = Number(spec.slice(i + 1));
  if (!Number.isInteger(port) || port <= 0 || port > 65535) {
    throw new Error(`PROVEO_CHROME_BRIDGE has no usable port: ${JSON.stringify(spec)}`);
  }
  return { host: spec.slice(0, i), port };
}

function log(msg) {
  process.stderr.write(`[proveo chrome-bridge] ${msg}\n`);
}

function main() {
  const spec = process.env.PROVEO_CHROME_BRIDGE || "";
  const token = process.env.PROVEO_CHROME_BRIDGE_TOKEN || "";
  if (!spec) {
    log("PROVEO_CHROME_BRIDGE is unset; nothing to relay");
    process.exit(2);
  }
  if (!token) {
    log("PROVEO_CHROME_BRIDGE_TOKEN is unset; refusing to relay without a handshake token");
    process.exit(2);
  }
  const target = parseTarget(spec);
  const dir = socketDir();
  const sock = path.join(dir, `${process.pid}.sock`);

  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  fs.chmodSync(dir, 0o700);
  try { fs.unlinkSync(sock); } catch (_) { /* fresh path */ }

  const server = net.createServer((client) => {
    const upstream = net.connect({ host: target.host, port: target.port });
    upstream.setNoDelay(true);
    client.setNoDelay(true);
    let closed = false;
    const close = (why) => {
      if (closed) return;
      closed = true;
      if (why) log(why);
      client.destroy();
      upstream.destroy();
    };
    upstream.once("connect", () => {
      upstream.write(HANDSHAKE_PREFIX + token + "\n");
      client.pipe(upstream);
      upstream.pipe(client);
    });
    upstream.once("error", (e) => close(`host relay ${spec} unreachable: ${e.message}`));
    client.once("error", (e) => close(`client error: ${e.message}`));
    upstream.once("close", () => close());
    client.once("close", () => close());
  });

  server.once("error", (e) => {
    log(`cannot listen on ${sock}: ${e.message}`);
    process.exit(1);
  });
  server.listen(sock, () => {
    fs.chmodSync(sock, 0o600);
    // Claude Code reads this to tell a relay socket from a native host's own; the
    // path is the contract, so print exactly that and nothing else on stdout.
    process.stdout.write(sock + "\n");
  });

  const shutdown = () => {
    server.close();
    try { fs.unlinkSync(sock); } catch (_) { /* already gone */ }
    process.exit(0);
  };
  process.on("SIGTERM", shutdown);
  process.on("SIGINT", shutdown);
  process.on("SIGHUP", shutdown);
}

if (require.main === module) {
  main();
}

module.exports = { HANDSHAKE_PREFIX, SOCKET_DIR_PREFIX, bridgeUsername, socketDir, parseTarget };
