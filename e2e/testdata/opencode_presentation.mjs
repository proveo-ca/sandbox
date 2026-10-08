// SPEC: _spec/defs/opencode/native-v2-integration.puml
import assert from "node:assert/strict"
import { spawn } from "node:child_process"
import { mkdir, readFile, writeFile } from "node:fs/promises"
import net from "node:net"
import path from "node:path"
import { setTimeout as pause } from "node:timers/promises"

const [mode, value, python, runtime, native, root] = process.argv.slice(2)
async function freePort() {
  const server = net.createServer()
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve))
  const port = server.address().port
  await new Promise(resolve => server.close(resolve))
  return port
}
const backendPort = await freePort(), uiPort = await freePort()
const registry = path.join(root, "drive")
await mkdir(registry, { recursive: true })
await writeFile(path.join(registry, "fixture.json"), JSON.stringify({ endpoints: {
  ui: `ws://127.0.0.1:${uiPort}`, backend: `ws://127.0.0.1:${backendPort}`,
}, viewport: { cols: 160, rows: 60 } }))
const config = {
  plugins: [], mcp: { servers: {} }, update: "disable",
  providers: { fixture: { package: "@opencode/ai/providers/openai-compatible",
    settings: { baseURL: "https://api.openai.com/v1", apiKey: "synthetic" },
    models: { model: { name: "Offline fixture", limit: { context: 100000, output: 4096 } } },
  } },
  agents: { build: { permissions: [
    { action: "shell", resource: "*", effect: "ask" },
    ...(value === "deny" ? [{ action: "shell", resource: "tee *", effect: "deny" }] : []),
  ] } },
}
const args = mode === "permission"
  ? ["run", "--format", "json", "--model", "fixture/model", "--agent", "build", "offline permission fixture"]
  : []
const direct = value === "prompt"
const child = spawn(direct ? native : python,
  direct ? [...args, "--standalone"] : ["-B", runtime, native, ...args], {
    cwd: root,
    env: { ...process.env, OPENCODE_SIMULATE: "1", OPENCODE_DRIVE: "fixture",
      OPENCODE_DRIVE_RENDERER: "headless", DRIVE_REGISTRY_DIR: registry,
      OPENCODE_CONFIG_CONTENT: JSON.stringify(config),
    }, stdio: ["ignore", "pipe", "pipe"],
  })
let stdout = "", stderr = "", screen = "", exit
child.stdout.on("data", chunk => { stdout += chunk })
child.stderr.on("data", chunk => { stderr += chunk })
const exited = new Promise(resolve => child.once("close", (code, signal) => {
  exit = { code, signal }
  resolve(exit)
}))
const timer = setTimeout(() => child.kill("SIGTERM"), 35000)
const sockets = []
async function connect(port) {
  for (let attempt = 0; attempt < 400 && !exit; attempt++) {
    const candidate = new WebSocket(`ws://127.0.0.1:${port}`)
    const opened = await new Promise(resolve => {
      candidate.onopen = () => resolve(true)
      candidate.onerror = () => resolve(false)
    })
    if (!opened) { candidate.close(); await pause(50); continue }
    sockets.push(candidate)
    let sequence = 0
    const pending = new Map()
    const rpc = {
      call(method, params) {
        const id = ++sequence
        return new Promise((resolve, reject) => {
          const timeout = setTimeout(() => { pending.delete(id); reject(new Error(`RPC timeout: ${method}`)) }, 10000)
          pending.set(id, { resolve, reject, timeout })
          candidate.send(JSON.stringify({ jsonrpc: "2.0", id, method, ...(params ? { params } : {}) }))
        })
      },
      notification: async () => {},
    }
    candidate.onmessage = message => {
      const event = JSON.parse(message.data)
      if (event.id !== undefined) {
        const waiter = pending.get(event.id)
        pending.delete(event.id)
        if (!waiter) return
        clearTimeout(waiter.timeout)
        if (event.error) waiter.reject(new Error(JSON.stringify(event.error)))
        else waiter.resolve(event.result)
      } else rpc.notification(event).catch(error => { stderr += String(error); child.kill("SIGTERM") })
    }
    return rpc
  }
  throw new Error(`native ${mode} simulator did not start:\n${stdout}\n${stderr}`)
}
async function capture(ui) {
  const frame = await ui.call("ui.capture")
  screen = frame.lines.map(line => line.spans.map(span => span.text).join("")).join("\n")
  return screen
}
async function until(check, description) {
  for (let attempt = 0; attempt < 200 && !exit; attempt++) {
    const last = await check()
    if (last) return last
    await pause(50)
  }
  throw new Error(`timed out: ${description}\n${screen}\n${stdout}\n${stderr}`)
}
try {
  if (mode === "permission") {
    const backend = await connect(backendPort)
    let shellCalls = 0, turns = 0, results = []
    backend.notification = async event => {
      if (event.method === "tool.invocation") throw new Error("the fixture must execute the native shell tool")
      if (event.method !== "llm.request") return
      turns++
      const tools = event.params.body.tools ?? []
      const toolResults = event.params.body.messages.filter(message =>
        message.role === "tool" && message.tool_call_id === "call_offline_shell")
      if (toolResults.length) results = toolResults
      let items
      if (shellCalls === 0 && tools.some(tool => tool.function?.name === "shell")) {
        shellCalls++
        const schema = tools.find(tool => tool.function?.name === "shell").function.parameters
        assert.ok(schema.properties.command, JSON.stringify(schema))
        items = [{ type: "toolCall", index: 0, id: "call_offline_shell", name: "shell", input: {
          command: "printf 'offline shell permission fixture\\n' | tee shell-executed", workdir: root,
        } }]
      } else {
        items = [{ type: "textDelta", text: "offline permission fixture complete" }]
      }
      await backend.call("llm.chunk", { id: event.params.id, items })
      await backend.call("llm.finish", { id: event.params.id, reason: items[0].type === "toolCall" ? "tool-calls" : "stop" })
    }
    await backend.call("llm.attach")
    await exited
    assert.equal(exit.code, 0, `${stdout}\n${stderr}`)
    assert.equal(shellCalls, 1, `native shell tool was not offered:\n${stdout}\n${stderr}`)
    assert.ok(turns >= 2, `no native tool result: ${stdout}\n${stderr}`)
    assert.ok(results.length, `no native shell result:\n${stdout}\n${stderr}`)
    const result = JSON.stringify(results)
    if (value === "ask") {
      assert.equal(results[0].content, "offline shell permission fixture\n")
      assert.equal(await readFile(path.join(root, "shell-executed"), "utf8"), "offline shell permission fixture\n")
    } else {
      const error = JSON.parse(results[0].content).error
      assert.equal(error.type, "permission.rejected", result)
      if (value === "deny") assert.equal(error.message, "Permission denied: shell")
      else assert.match(error.message, /non-interactive run cannot ask the user for permission/)
    }
    console.log(JSON.stringify({ mode, value, turns, shellCalls, toolResult: results,
      ...(value === "ask" ? { shellOutput: "offline shell permission fixture\n" } : {}),
    }))
  } else {
    const ui = await connect(uiPort)
    await until(async () => (await capture(ui)).includes("Ask anything"), "native TUI ready")
    await pause(300)
    await ui.call("ui.press", { key: "p", modifiers: { ctrl: true } })
    await ui.call("ui.type", { text: "Open settings" })
    await ui.call("ui.enter")
    await until(async () => (await capture(ui)).includes("Settings"), "native settings dialog")
    const checks = value === "inline"
      ? { Permissions: "auto accept", Markdown: "source", Thinking: "show", "Tool grouping": "none",
        Verbosity: "high", Sidebar: "auto", Mode: "on", Indicators: "status icons" }
      : { Permissions: "prompt", Markdown: "rendered", Thinking: "hide", "Tool grouping": "auto",
        Verbosity: "low", Sidebar: "hide", Mode: "off", Indicators: "always show numbers" }
    const observed = {}
    for (const [title, expected] of Object.entries(checks)) {
      await ui.call("ui.press", { key: "u", modifiers: { ctrl: true } })
      await ui.call("ui.type", { text: title })
      let screen
      await until(async () => {
        screen = await capture(ui)
        return screen.split("\n").some(line => line.trim().startsWith(title) && line.trim().endsWith(expected))
      }, `setting ${title} = ${expected}`)
      observed[title] = screen.split("\n").find(line =>
        line.trim().startsWith(title) && line.trim().endsWith(expected)).trim()
    }
    await ui.call("ui.press", { key: "escape" })
    await ui.call("ui.press", { key: "c", modifiers: { ctrl: true } })
    await ui.call("ui.press", { key: "c", modifiers: { ctrl: true } })
    await exited
    assert.equal(exit.code, 0, `${stdout}\n${stderr}`)
    console.log(JSON.stringify({ mode, value, observed }))
  }
} finally {
  clearTimeout(timer)
  for (const socket of sockets) socket.close()
  if (!exit) {
    child.kill("SIGTERM")
    const kill = setTimeout(() => child.kill("SIGKILL"), 5000)
    await exited
    clearTimeout(kill)
  }
}
