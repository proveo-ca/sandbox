# CLAUDE.md — Claude Code Working Rules (ML Blackbox Loop)

You are operating as a machine-learning execution loop inside a container sandbox.

## Required Loop Pattern
For every non-trivial task:

1. **Goal & Acceptance Criteria** (1-2 sentences). What must be true for this task to be complete?
2. **Verification Commands**. Identify the exact commands that prove success (test, lint, typecheck, build). Run them before editing if possible.
3. **Design First**. For non-trivial work, get a file plan from `architect` before editing. Skip only for a contained one-file change.
4. **Smallest Verifiable Step**. Make the smallest change that can be verified.
5. **Execute & Inspect**. Run the verification command(s). If they fail, read the output, form a new hypothesis, and repeat.
6. **Review Gates**. Before declaring completion, hand the diff to `adversarial-reviewer` — always — and to `security-reviewer` when the triggers below fire. `[BLOCKER]` and `[HIGH]` findings are completion blockers.
7. **Spec Sync**. If `_spec/`, planning docs, architecture boundaries, or harness contracts changed, hand the diff to `spec-keeper`.
8. **Stopping Condition**. Stop only when verification passes and the gates are clear, or the human explicitly stops you.

## Subagents
The image seeds five subagents into `~/.claude/agents/`. All are read-only except
`spec-keeper`, which may write only `_spec/`, `PLAN.md`, `CLAUDE.md`, and `AGENTS.md`.
None of them has `Bash` — **you** run `git diff` and hand them the diff and the paths;
they read, they never edit, and they never verify on your behalf.

| Subagent | Invoke it when |
| --- | --- |
| `architect` | Before implementing anything non-trivial — new module, new contract, more than a couple of files. |
| `monorepo-coordinator` | The change spans more than one project, touches a shared package, or moves a project boundary. |
| `adversarial-reviewer` | Always, on the final diff, before you claim the task is done. |
| `security-reviewer` | Auth, secrets, network, dependencies, sandbox posture, permissions, payments, user data, or serialization are touched. |
| `spec-keeper` | `_spec/`, `PLAN.md`, `CLAUDE.md`/`AGENTS.md`, architecture boundaries, or harness contracts changed. |

A reviewer answering `READY TO MERGE: no` means you fix the finding or ask the human to
accept the risk explicitly. Do not ask a reviewer to fix its own findings on the first pass.

## Constraints
- Never claim success without running the relevant verification command.
- Prefer many small loops over one large edit.
- When verification fails, always show the failing output before proposing the next change.
- You have full tool access via `--dangerously-skip-permissions` because the container is the security boundary. The subagents deliberately do not — that read-only split is the point.

## Output Discipline
After each iteration, state:
- What you changed
- Which verification command you ran
- The result (pass/fail + key output)
- Next hypothesis or "DONE" if verification passes
