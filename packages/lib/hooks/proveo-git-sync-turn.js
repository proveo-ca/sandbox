// SPEC: _spec/packages/lib/git-sync-turn.puml
import { spawn } from "node:child_process"
import { isAbsolute } from "node:path"

export default {
  id: "proveo-git-sync-turn",
  setup(ctx) {
    if (process.env.PROVEO_GIT_SYNC_MSG_INFLIGHT === "1") return

    const script = process.env.PROVEO_GIT_SYNC_HOOK || "/opt/proveo/hooks/git-sync-turn.sh"
    const controller = new AbortController()
    const idle = new Set()
    const pending = new Map()
    let jobs = Promise.resolve()
    let child

    const report = (error) => {
      if (!controller.signal.aborted) console.error("[proveo] git-sync-turn:", error)
    }

    const run = (cwd) => new Promise((resolve, reject) => {
      const current = spawn("bash", [script], {
        cwd,
        env: { ...process.env, GIT_TERMINAL_PROMPT: "0", PROVEO_GIT_SYNC_DIALECT: "idle" },
        stdio: "ignore",
        detached: true,
      })
      child = current
      current.once("error", reject)
      current.once("close", (code, signal) => {
        if (child === current) child = undefined
        if (code === 0) resolve()
        else reject(new Error(`hook exited ${signal || code}`))
      })
    })

    const events = (async () => {
      for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
        if (controller.signal.aborted) break
        const sessionID = event.data?.sessionID
        if (!sessionID) continue
        if (event.type === "session.status" && event.data.status.type !== "idle") {
          idle.delete(sessionID)
          continue
        }
        if (event.type !== "session.idle" && event.type !== "session.status") continue
        if (idle.has(sessionID)) continue
        idle.add(sessionID)
        pending.set(sessionID, event)
        jobs = jobs.then(async () => {
          const next = pending.get(sessionID)
          pending.delete(sessionID)
          if (!next || controller.signal.aborted) return
          const cwd = next.location?.directory || (await ctx.session.get({ sessionID })).location?.directory
          if (controller.signal.aborted) return
          if (!cwd || !isAbsolute(cwd)) throw new Error(`no workspace for session ${sessionID}`)
          await run(cwd)
        }).catch(report)
      }
    })().catch(report)

    return async () => {
      controller.abort()
      pending.clear()
      const stopped = child?.pid ? new Promise((resolve) => {
        const pid = child.pid
        const kill = (signal) => {
          try { process.kill(-pid, signal) } catch {}
        }
        kill("SIGTERM")
        setTimeout(() => {
          kill("SIGKILL")
          resolve()
        }, 1000)
      }) : Promise.resolve()
      await Promise.all([events, jobs, stopped])
    }
  },
}
