export const ProveoGitSyncTurn = async ({ $, directory, worktree }) => {
  const script = process.env.PROVEO_GIT_SYNC_HOOK || "/opt/proveo/hooks/git-sync-turn.sh"
  return {
    event: async ({ event }) => {
      if (event.type !== "session.idle") return
      const cwd = worktree || directory || process.cwd()
      await $`env GIT_TERMINAL_PROMPT=0 PROVEO_GIT_SYNC_DIALECT=idle bash ${script} </dev/null`.cwd(cwd).nothrow().quiet()
    },
  }
}
