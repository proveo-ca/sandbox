// SPEC: _spec/defs/opencode/native-v2-integration.puml
import assert from "node:assert/strict"
import { spawn } from "node:child_process"
import { writeFile, mkdir } from "node:fs/promises"
import net from "node:net"
import path from "node:path"
import { setTimeout as pause } from "node:timers/promises"

const [python, wrapper, native, root] = process.argv.slice(2)
const server = net.createServer()
await new Promise(resolve => server.listen(0, "127.0.0.1", resolve))
const port = server.address().port
await new Promise(resolve => server.close(resolve))
const registry = path.join(root, "drive")
await mkdir(registry)
await writeFile(path.join(registry, "fixture.json"), JSON.stringify({ endpoints: {
  ui: `ws://127.0.0.1:${port + 1}`, backend: `ws://127.0.0.1:${port}`,
} }))
const config = {
  plugins: [],
  providers: { fixture: { package: "@opencode/ai/providers/openai-compatible",
    settings: { baseURL: "https://api.openai.com/v1", apiKey: "synthetic" },
    models: { model: { name: "Offline fixture", limit: { context: 100000, output: 4096 } } },
  } },
}
const child = spawn(python, ["-B", wrapper, native, "run", "--auto", "--model", "fixture/model", "offline truncation fixture"], {
  cwd: root,
  env: { ...process.env, OPENCODE_SIMULATE: "1", OPENCODE_DRIVE: "fixture", DRIVE_REGISTRY_DIR: registry,
    OPENCODE_CONFIG_CONTENT: JSON.stringify(config),
  },
  stdio: ["ignore", "pipe", "pipe"],
})
let stdout = "", stderr = ""
child.stdout.on("data", chunk => { stdout += chunk })
child.stderr.on("data", chunk => { stderr += chunk })
const exited = new Promise(resolve => child.once("close", code => resolve(code)))
const timeout = setTimeout(() => child.kill("SIGTERM"), 30000)
let socket
for (let attempt = 0; attempt < 200 && child.exitCode === null; attempt++) {
  const candidate = new WebSocket(`ws://127.0.0.1:${port}`)
  const opened = await new Promise(resolve => {
    candidate.onopen = () => resolve(true)
    candidate.onerror = () => resolve(false)
  })
  if (opened) { socket = candidate; break }
  await pause(50)
}
assert.ok(socket, `native simulator did not start: ${stderr}`)
let sequence = 0, turns = 0, toolCalls = 0
const pending = new Map()
function call(method, params) {
  const id = ++sequence
  const reply = new Promise((resolve, reject) => pending.set(id, { resolve, reject }))
  socket.send(JSON.stringify({ jsonrpc: "2.0", id, method, ...(params ? { params } : {}) }))
  return reply
}
socket.onmessage = async message => {
  const event = JSON.parse(message.data)
  if (event.id !== undefined) {
    const waiter = pending.get(event.id)
    pending.delete(event.id)
    if (event.error) waiter?.reject(new Error(JSON.stringify(event.error)))
    else waiter?.resolve(event.result)
    return
  }
  if (event.method === "llm.request") {
    turns++
    const tools = event.params.body.tools ?? []
    const execute = tools.some(tool => tool.function?.name === "execute") && toolCalls === 0
    if (execute) toolCalls++
    const items = execute
      ? [{ type: "toolCall", index: 0, id: "call_fixture", name: "execute", input: { code: "console.log('native truncation fixture\\n'.repeat(5000))" } }]
      : [{ type: "textDelta", text: "offline fixture complete" }]
    await call("llm.chunk", { id: event.params.id, items })
    await call("llm.finish", { id: event.params.id, reason: execute ? "tool-calls" : "stop" })
  }
  if (event.method === "tool.invocation") {
    toolCalls++
    await call("tool.finish", { id: event.params.id, output: {
      structured: null, content: [{ type: "text", text: "native truncation fixture\n".repeat(5000) }],
    } })
  }
}
await call("llm.attach")
const code = await exited
clearTimeout(timeout)
socket.close()
assert.equal(code, 0, stderr)
assert.equal(toolCalls, 1, `native tool was not executed: ${stdout}\n${stderr}`)
console.log(JSON.stringify({ turns, toolCalls }))
