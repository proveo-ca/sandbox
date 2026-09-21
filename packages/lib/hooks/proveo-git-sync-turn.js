export const ProveoGitSyncTurn = async ({ $, directory, worktree }) => {
  const script = process.env.PROVEO_GIT_SYNC_HOOK || "/opt/proveo/hooks/git-sync-turn.sh"
  return {
    event: async ({ event }) => {
      if (event.type !== "session.idle") return
      const cwd = worktree || directory || process.cwd()
      await $`env PROVEO_GIT_SYNC_DIALECT=idle bash ${script}`.cwd(cwd).nothrow()
    },
  }
}
