# base-node-browser

`proveo/base-node-browser` = `proveo/base-node` + Playwright and one headless Chromium. It inherits the pinned
`agent-browser` client from `proveo/base`.

`proveo run <agent> --addon chrome` starts or reuses the dedicated Chrome profile on the host
and connects the sandbox client to it at launch. Choose `--addon browser` for Chromium inside sbx.
The run's host viewport and guest agent-browser attach to the same Chromium process. Use
`--browser-profile <name>` to retain its profile inside the owning sandbox.

| client | what it is for | how it finds the browser |
| --- | --- | --- |
| Playwright `1.61.0` (CLI + library) | the project's own test suites, `npx playwright …` | `PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright` |
| [vercel-labs/agent-browser](https://github.com/vercel-labs/agent-browser) `0.36.0` (native Rust CLI) | the agent's interactive tool: `open`, `snapshot`, `click`, `fill`, `screenshot` over CDP, with accessibility-tree refs | `AGENT_BROWSER_EXECUTABLE_PATH=/opt/proveo/chromium/chrome`, a revision-stable symlink into Playwright's store |

One Chromium, two clients. `agent-browser install` is never run: it would download a second
browser (Chrome for Testing). agent-browser's bundled skills live at
`AGENT_BROWSER_SKILLS_DIR=/opt/agent-browser/skill-data`, so `agent-browser skills get core`
serves the guide matching the installed binary.

- `proveo/base-node-browser` is FROM `proveo/base-node`. The `<harness>-browser` variants
  (`opencode-browser`, `claudecode-browser`, `codex-browser`, `cursor-browser`;
  `mise run build <harness>-browser`) build this Dockerfile on `proveo/<harness>` instead.
- Not a runnable harness — a `mise run build` / `mise run deploy` target like `base`.
- The floor probe (`imagebuild.Specs["base-node-browser"].Floor`) checks Playwright, the Chromium store, `agent-browser`, its
  executable path, its skills tree and the seeded skill stub; a `:local` image missing any of
  them is rebuilt.

## How agents learn it is there

The base image bakes a discovery stub at `/opt/proveo/skills/agent-browser/SKILL.md`. At seed time
(`proveo_seed` → `proveo_seed_browser_skills`, both backends) it is copied into the
harness's **user-level** skills directory. Codex uses `~/.agents/skills`, Claude Code
uses `~/.claude/skills`, Cursor uses `~/.cursor/skills`, and OpenCode uses
`~/.config/opencode/skills`. The stub tells the agent to run
`agent-browser skills get core` and not to install anything. Opt out with
`PROVEO_BROWSER_SKILL=off`. The seed copies the skill when the run selects host
Chrome or the image provides sandbox Chromium.

## Pins

`agent-browser` comes from the pinned npm tarball (`AGENT_BROWSER_VERSION` +
`AGENT_BROWSER_SHA256` build args), not `npm install -g`: the package declares Node ≥ 24 on
this Node 22 floor, its JS launcher only execs the native binary, and only this arch's binary
is kept. Bump both args together; `internal/contract` checks the shape.

Spec: `_spec/defs/browser-layer.puml`.
