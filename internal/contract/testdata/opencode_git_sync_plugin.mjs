import assert from "node:assert/strict"
import { spawn } from "node:child_process"
import { EventEmitter } from "node:events"
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { isAbsolute, join } from "node:path"
import { setImmediate as tick } from "node:timers/promises"
import { test } from "node:test"
import vm from "node:vm"

const source = await readFile(new URL("../../../packages/lib/hooks/proveo-git-sync-turn.js", import.meta.url), "utf8")

async function load(spawnImpl, env = {}, kill = () => {}) {
  const errors = []
  const context = vm.createContext({
    process: { env, kill },
    AbortController,
    setTimeout,
    console: { error: (...args) => errors.push(args.map(String).join(" ")) },
  })
  const module = new vm.SourceTextModule(source, { context })
  await module.link((name) => {
    const exports = name === "node:child_process" ? { spawn: spawnImpl } : { isAbsolute }
    return new vm.SyntheticModule(Object.keys(exports), function () {
      for (const [key, value] of Object.entries(exports)) this.setExport(key, value)
    }, { context })
  })
  await module.evaluate()
  return { plugin: module.namespace.default, errors }
}

function feed() {
  const queued = []
  let waiter
  let closed = false
  let failure
  const stream = {
    received: 0,
    subscriptions: 0,
    push(event) {
      queued.push(event)
      waiter?.()
    },
    fail(error) {
      failure = error
      waiter?.()
    },
    subscribe({ signal }) {
      stream.subscriptions++
      stream.signal = signal
      signal.addEventListener("abort", () => {
        closed = true
        waiter?.()
      }, { once: true })
      return {
        [Symbol.asyncIterator]() { return this },
        async next() {
          if (!closed && !failure && !queued.length) await new Promise((resolve) => { waiter = resolve })
          waiter = undefined
          if (failure) throw failure
          if (closed) return { done: true }
          stream.received++
          return { value: queued.shift(), done: false }
        },
        async return() {
          closed = true
          return { done: true }
        },
      }
    },
  }
  return stream
}

async function until(predicate) {
  const deadline = Date.now() + 5000
  while (!predicate()) {
    assert.ok(Date.now() < deadline, "timed out waiting for plugin lifecycle")
    await tick()
  }
  await tick()
}

function status(sessionID, type, directory) {
  return { type: "session.status", data: { sessionID, status: { type } }, ...(directory ? { location: { directory } } : {}) }
}

function children() {
  const calls = []
  const stub = (command, args, options) => {
    const child = new EventEmitter()
    child.pid = 1000 + calls.length
    calls.push({ command, args, options, child })
    return child
  }
  return { calls, stub }
}

test("native setup consumes direct events and serializes eventual idle work", async () => {
  const stream = feed()
  const { calls, stub } = children()
  const lookups = []
  const { plugin, errors } = await load(stub, { PROVEO_GIT_SYNC_HOOK: "/hook with spaces.sh", GIT_TERMINAL_PROMPT: "1" })
  assert.equal(plugin.id, "proveo-git-sync-turn")
  const cleanup = plugin.setup({
    event: stream,
    location: { directory: "/server" },
    session: { async get(input) { lookups.push(input.sessionID); return { location: { directory: "/session" } } } },
  })
  assert.equal(typeof cleanup, "function")
  assert.equal(stream.subscriptions, 1)
  stream.push({ event: { type: "session.idle", data: { sessionID: "v1" } } })
  stream.push(status("a", "busy", "/event"))
  stream.push(status("a", "idle", "/event"))
  stream.push({ type: "session.idle", data: { sessionID: "a" }, location: { directory: "/event" } })
  stream.push(status("b", "idle"))
  stream.push(status("b", "idle"))
  await until(() => stream.received === 6 && calls.length === 1)
  assert.equal(calls[0].command, "bash")
  assert.deepEqual(Array.from(calls[0].args), ["/hook with spaces.sh"])
  assert.equal(calls[0].options.cwd, "/event")
  assert.equal(calls[0].options.stdio, "ignore")
  assert.equal(calls[0].options.detached, true)
  assert.equal(calls[0].options.env.GIT_TERMINAL_PROMPT, "0")
  assert.equal(calls[0].options.env.PROVEO_GIT_SYNC_DIALECT, "idle")
  assert.deepEqual(lookups, [])
  calls[0].child.emit("close", 0)
  await until(() => calls.length === 2)
  assert.equal(calls[1].options.cwd, "/session")
  assert.deepEqual(lookups, ["b"])
  calls[1].child.emit("close", 0)
  stream.push(status("a", "busy"))
  stream.push(status("a", "idle", "/next-turn"))
  await until(() => calls.length === 3)
  assert.equal(calls[2].options.cwd, "/next-turn")
  calls[2].child.emit("close", 0)
  await cleanup()
  assert.deepEqual(errors, [])
})

test("lookup, spawn, subprocess and stream failures stay best effort", async () => {
  const stream = feed()
  const { calls, stub } = children()
  let attempts = 0
  const { plugin, errors } = await load((...args) => {
    if (++attempts === 1) throw new Error("spawn threw")
    return stub(...args)
  })
  const cleanup = plugin.setup({ event: stream, session: { async get() { throw new Error("lookup failed") } } })
  stream.push(status("lookup", "idle"))
  stream.push(status("relative", "idle", "relative/path"))
  stream.push(status("throws", "idle", "/workspace"))
  stream.push(status("spawn-error", "idle", "/workspace"))
  stream.push(status("exit-error", "idle", "/workspace"))
  stream.push(status("success", "idle", "/workspace"))
  await until(() => calls.length === 1)
  calls[0].child.emit("error", new Error("spawn error event"))
  calls[0].child.emit("close", -2)
  await until(() => calls.length === 2)
  calls[1].child.emit("close", 7)
  await until(() => calls.length === 3)
  calls[2].child.emit("close", 0)
  stream.fail(new Error("subscription failed"))
  await until(() => errors.length === 6)
  await cleanup()
  for (const message of ["lookup failed", "no workspace", "spawn threw", "spawn error event", "hook exited 7", "subscription failed"]) {
    assert.ok(errors.some((error) => error.includes(message)), message)
  }
})

test("inflight subject helpers never subscribe or spawn", async () => {
  const { plugin, errors } = await load(() => assert.fail("recursive helper spawned"), { PROVEO_GIT_SYNC_MSG_INFLIGHT: "1" })
  assert.equal(plugin.setup({}), undefined)
  assert.deepEqual(errors, [])
})

test("cleanup aborts subscription, discards queued work and kills the subprocess group", async () => {
  const stream = feed()
  const { calls, stub } = children()
  const kills = []
  const { plugin, errors } = await load(stub, {}, (pid, signal) => {
    kills.push([pid, signal])
    if (signal === "SIGKILL") calls[0].child.emit("close", null, signal)
  })
  const cleanup = plugin.setup({ event: stream })
  stream.push(status("active", "idle", "/workspace"))
  stream.push(status("queued", "idle", "/other"))
  await until(() => stream.received === 2 && calls.length === 1)
  await cleanup()
  assert.equal(stream.signal.aborted, true)
  assert.deepEqual(kills, [[-1000, "SIGTERM"], [-1000, "SIGKILL"]])
  assert.equal(calls.length, 1)
  assert.deepEqual(errors, [])
})

test("cleanup prevents spawning after an outstanding session lookup", async () => {
  const stream = feed()
  let resolveLookup
  const { plugin, errors } = await load(() => assert.fail("spawned after cleanup"))
  const cleanup = plugin.setup({
    event: stream,
    session: { get() { return new Promise((resolve) => { resolveLookup = resolve }) } },
  })
  stream.push(status("lookup", "idle"))
  await until(() => resolveLookup)
  const stopped = cleanup()
  resolveLookup({ location: { directory: "/session" } })
  await stopped
  assert.deepEqual(errors, [])
})

test("real Node subprocess receives workspace, noninteractive env and EOF stdin", async () => {
  const directory = await mkdtemp(join(tmpdir(), "proveo-plugin-"))
  const script = join(directory, "hook with spaces.sh")
  const marker = join(directory, "completed")
  await writeFile(script, `#!/usr/bin/env bash
if IFS= read -r input; then exit 7; fi
printf '%s\\n' "$PWD" "$GIT_TERMINAL_PROMPT" "$PROVEO_GIT_SYNC_DIALECT" > completed
`)
  const stream = feed()
  const { plugin, errors } = await load(spawn, { PATH: process.env.PATH, PROVEO_GIT_SYNC_HOOK: script }, process.kill.bind(process))
  const cleanup = plugin.setup({ event: stream })
  try {
    stream.push(status("real", "idle", directory))
    let output
    const deadline = Date.now() + 5000
    while (!output) {
      try { output = await readFile(marker, "utf8") } catch {}
      assert.ok(Date.now() < deadline, "real hook did not complete")
      await tick()
    }
    assert.equal(output, `${directory}\n0\nidle\n`)
  } finally {
    await cleanup()
    await rm(directory, { recursive: true, force: true })
  }
  assert.deepEqual(errors, [])
})
