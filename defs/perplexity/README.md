# Perplexity Computer (`pplx`) — experimental

Subscription search harness wrapping Perplexity's [pplx CLI](https://docs.perplexity.ai/docs/cli/overview).

**Status:** experimental.

## What you get

- `pplx` installed immutably under `/opt/pplx-dist` (linked at `/usr/local/bin/pplx`)
- Interactive shell (no args) so you can `pplx auth login`, or forward args: `proveo run perplexity -- auth login`
- Host-side `.env` / broker forwarding for `PERPLEXITY_API_KEY` when set
- Durable proveo home for `~/.config/pplx` and `~/.config/perplexity` (login `credentials.json` scrubbed each run)

## Quick start

```bash
proveo run perplexity
# inside the sandbox:
pplx auth login
pplx search web "what is a bloom filter" -n 2
```

Or with a host key (preferred for durable auth):

```bash
# in project .env (gitignored) or a SAFE host secret store:
PERPLEXITY_API_KEY=pplx-...
proveo run perplexity -- search web "rust async runtimes"
```

## Authentication

| Method | How |
| ------ | --- |
| Interactive login | `pplx auth login` inside the sandbox (subscription) |
| API key (recommended) | Create at [console.perplexity.ai](https://console.perplexity.ai); set `PERPLEXITY_API_KEY` |

`PERPLEXITY_API_KEY` takes precedence over a key stored by `pplx auth login`. Proveo scrubs
`credentials.json` from proveo home each run — prefer the env key so auth never lands in the cache.

Subscription harnesses do **not** prompt for keys ahead of time: proveo warns when auth is
missing, runs the agent, and after the sandbox closes prints shell-specific setup hints
(project `.env` or a SAFE host location).

## Build / run

```bash
./build.sh
./run.sh
./run.sh -- search web "query"
```

Image: `proveo/perplexity:latest` (FROM `proveo/base`).
