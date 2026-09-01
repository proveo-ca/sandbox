ARG BASE_IMAGE=proveo/base-node-lsp:latest

# proveo-entrypoint is inherited from proveo/base-node (built once in defs/base).
FROM ${BASE_IMAGE}

ARG USER_ID=1000
ARG USER_NAME=claude

# Create or reuse the non-root user with specific UID/GID
RUN if getent passwd "${USER_ID}" >/dev/null; then \
      existing_user="$(getent passwd "${USER_ID}" | cut -d: -f1)"; \
      existing_group="$(getent group "${USER_ID}" | cut -d: -f1)"; \
      if [ "${existing_group}" != "${USER_NAME}" ]; then \
        groupmod -n "${USER_NAME}" "${existing_group}"; \
      fi; \
      if [ "${existing_user}" != "${USER_NAME}" ]; then \
        usermod -l "${USER_NAME}" -d "/home/${USER_NAME}" -m "${existing_user}"; \
      fi; \
      usermod -s /bin/bash "${USER_NAME}"; \
    else \
      groupadd -g "${USER_ID}" "${USER_NAME}" && \
      useradd -m -u "${USER_ID}" -g "${USER_ID}" -s /bin/bash "${USER_NAME}"; \
    fi

# sbx's built-in agent kits are authored against their own template, whose runtime
# user is `agent` with HOME=/home/agent: their setup.install chowns agent:agent,
# and the kit mounts five block volumes AT /home/agent/.claude/{projects,sessions,
# todos,shell-snapshots,statsig}.
#
# /home/agent must therefore be a REAL directory and the actual home. An alias
# plus a home SYMLINK is not enough and was the bug: a mount target cannot resolve
# through a symlink, so containerd materialises /home/agent as a fresh directory,
# shadowing the link. The volumes then land somewhere the agent never reads while
# $HOME points elsewhere — including `sessions`, which sbx's own kit calls
# load-bearing for `claude -c`. Measured: stock image holds a session, the same
# sbx invocation with this image died in 15s (e2e/ladder_test.go).
#
# So the REAL home is /home/agent and the harness-specific name is the alias —
# the reverse of what this used to do. /home/${USER_NAME} stays as a symlink
# because image healthchecks and def tests read paths through it, and nothing
# ever mounts there.
RUN usermod -d /home/agent -m "${USER_NAME}" \
 && if ! getent group agent >/dev/null; then groupadd -o -g "${USER_ID}" agent; fi \
 && if ! getent passwd agent >/dev/null; then \
      useradd -o -u "${USER_ID}" -g "${USER_ID}" -M -d /home/agent -s /bin/bash agent; \
    fi \
 && { [ -e "/home/${USER_NAME}" ] || ln -s /home/agent "/home/${USER_NAME}"; } \
 && chown -R "${USER_ID}:${USER_ID}" /home/agent

RUN apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends \
    wget python3 python3-pip \
    iptables \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden /usr /opt

# Static docker CLI only — the record of three attempts, so this is not re-derived.
#
# sbx gives each sandbox its own Docker daemon, but it runs from the IMAGE and
# starts as root. Shipping dockerd makes sbx ATTEMPT that start; a daemon that
# fails to start blocks the whole sandbox, which is worse than having none.
#
#  1. bookworm, nf_tables  → "failed to create NAT chain DOCKER (exit status 4)"
#  2. bookworm, legacy     → "can't initialize iptables table `nat'"
#  3. TRIXIE, nf_tables    → "iptables v1.8.11: Could not fetch rule set
#                             generation id: Permission denied (you must be root)"
#
# The trixie move fixed the version: iptables is now 1.8.11, identical to the
# stock template's, so the base image is no longer the blocker. What remains is
# the privilege. Scoped sudo (NOPASSWD for /usr/local/bin/dockerd only) was tried
# and changed NOTHING — the error is byte-identical — so sbx does not invoke
# dockerd through sudo, and the stock template's `NOPASSWD:ALL` is not what makes
# it work either. proveo-harden is not implicated: it only strips setuid/setgid
# and removes nc/netstat.
#
# Next step is sbx-side, not packaging: find how its dind path grants dockerd its
# privileges. e2e/ladder_test.go is the harness that would prove a fix.
#
# Keep the version in step with cursor, opencode and cecli.
RUN DOCKER_VERSION=29.7.2 \
    && case "$(uname -m)" in \
         x86_64|amd64) DOCKER_ARCH=x86_64 ;; \
         aarch64|arm64) DOCKER_ARCH=aarch64 ;; \
         *) echo "unsupported architecture for docker static client: $(uname -m)" >&2; exit 1 ;; \
       esac \
    && curl -fsSL "https://download.docker.com/linux/static/stable/${DOCKER_ARCH}/docker-${DOCKER_VERSION}.tgz" -o /tmp/docker.tgz \
    && tar -xzf /tmp/docker.tgz -C /tmp \
    && install -m 0755 /tmp/docker/* /usr/local/bin/ \
    && ln -sf /usr/local/bin/dockerd /usr/bin/dockerd \
    && rm -rf /tmp/docker /tmp/docker.tgz

# ── Docker-in-sandbox ───────────────────────────────────────────────────────
#
# sbx gives each sandbox its own private Docker daemon, and it decides whether to
# start one from an IMAGE LABEL — `com.docker.sandboxes.start-docker`, which its
# own templates carry. Without the label sbx records `dind: false` at container
# creation and never starts a daemon, which is why every proveo sbx run reported
# "Cannot connect to the Docker daemon" beside Compose files it could read.
#
# The daemon binaries were already being downloaded and discarded: the static
# tarball carries dockerd, containerd, runc and friends alongside the client.
RUN groupadd -f docker \
 && usermod -aG docker "${USER_NAME}" \
 && usermod -aG docker agent
LABEL com.docker.sandboxes.start-docker="true"



# Install basic Python tools
RUN pip3 install --no-cache-dir --break-system-packages \
    requests \
    python-dotenv

# Install Node.js tools
RUN npm install -g --no-optional \
    typescript \
    ts-node \
    prettier \
    eslint \
    && npm cache clean --force

# Install Claude Code
RUN npm install -g @anthropic-ai/claude-code && npm cache clean --force

# ── Code-intelligence plugins, pre-seeded ───────────────────────────────────
# SPEC: _spec/defs/claudecode/lsp-plugins-seed.puml
#
# Claude Code offers to install an official `*-lsp` plugin whenever it finds that
# plugin's language-server binary on PATH with the plugin absent ("If you already
# have a language server installed, Claude may prompt you to install the
# corresponding plugin when you open a project" — Discover and install plugins,
# Code intelligence). This image bakes the binaries, so every session opened with
# an install prompt for a plugin nobody asked for, and a sandbox home could never
# keep the answer. The documented remedy is a SEED: a read-only mirror of
# ~/.claude/plugins that Claude Code reads in place — marketplace registration
# and plugin caches, no network at run time, valid for whatever HOME a run uses
# (CLAUDE_CODE_PLUGIN_SEED_DIR). Whether a seeded plugin is ENABLED is a settings
# matter, decided by the seed step per binary actually present
# (proveo_enable_claude_lsp_plugins), so a plugin whose server this floor lacks
# never surfaces as "Executable not found". Measured on 2.1.251: the seed alone is
# NOT registered at startup ("marketplace not registered", 0 plugins), so the seed
# step also writes installed_plugins.json / known_marketplaces.json records into
# the home that point here. World-writable because Claude Code drops `.in_use/<pid>`
# markers beside a loaded plugin and the runtime uid is the operator's, not 1000;
# those writes land in the container's own layer.
#
# The list is pinned to _claude_lsp_plugins in packages/lib/entrypoint-lib.sh by
# internal/contract. kotlin-lsp is deliberately absent: it expects a `kotlin-lsp`
# binary this floor does not ship — proveo provisions fwcd's
# kotlin-language-server and covers Kotlin through proveo-lsp instead.
# Measured on proveo/claudecode:local, 2026-09-01: the marketplace clone takes
# ~16s and the whole seed is 9.9 MB.
ARG CLAUDE_PLUGIN_SEED=/opt/proveo/claude-plugins
ENV CLAUDE_CODE_PLUGIN_SEED_DIR=${CLAUDE_PLUGIN_SEED}
RUN set -e; \
    export HOME=/tmp/claude-build DISABLE_AUTOUPDATER=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
           CLAUDE_CODE_PLUGIN_CACHE_DIR="${CLAUDE_PLUGIN_SEED}"; \
    mkdir -p "$HOME" "${CLAUDE_PLUGIN_SEED}" \
    && claude plugin marketplace add anthropics/claude-plugins-official \
    && for p in typescript-lsp pyright-lsp gopls-lsp rust-analyzer-lsp clangd-lsp jdtls-lsp lua-lsp; do \
         claude plugin install "${p}@claude-plugins-official"; \
       done \
    && test -f "${CLAUDE_PLUGIN_SEED}/known_marketplaces.json" \
    && test -d "${CLAUDE_PLUGIN_SEED}/cache/claude-plugins-official/typescript-lsp" \
    && chmod -R a+rwX "${CLAUDE_PLUGIN_SEED}" \
    && rm -rf /tmp/claude-build

# Setup secure workspace with proper permissions.
#
# The workspace root is /app, the same as every other def. /workspace survives
# only for the parts that were never claudecode's own: --data-dir mounts at
# /workspace/data for EVERY harness, so that path is a cross-harness convention
# rather than a leftover of the retired input-output layout.
RUN mkdir -p /app \
    && mkdir -p /app/output \
    && mkdir -p /app/.claude \
    && mkdir -p /workspace/data \
    && mkdir -p /workspace/temp \
    && mkdir -p /workspace/mcp-servers \
    && mkdir -p /home/${USER_NAME}/.claude \
    && chown -R ${USER_NAME}:${USER_NAME} /app /workspace /home/${USER_NAME}/.claude \
    && chmod 755 /workspace \
    && chmod 750 /app \
    && chmod 750 /workspace/data \
    && chmod 755 /app/output \
    && chmod 755 /workspace/temp \
    && chmod 755 /workspace/mcp-servers

# Copy Claude configuration directly into final locations
# The sandbox theme, in proveo's own identity palette. Baked here and installed
# into the agent home by the seed, so it reaches BOTH backends.
COPY defs/claudecode/mcp/themes/ /opt/proveo/themes/
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/claude-config.json /home/${USER_NAME}/.claude.json
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/settings.local.json /home/${USER_NAME}/.claude/settings.local.json
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/settings.local.json /app/.claude/settings.local.json
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/proveo-lib/ /opt/proveo/lib/
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/defaults/ /opt/claudecode/defaults/
COPY --chown=${USER_NAME}:${USER_NAME} defs/subagents/ /opt/proveo/subagents/
COPY --chown=${USER_NAME}:${USER_NAME} defs/bridges/ /opt/proveo/bridges/

# Copy shared entrypoint
# proveo's own conventions, delivered as Claude Code's MANAGED POLICY CLAUDE.md.
#
# That path is the highest tier of the memory hierarchy: it loads before the user
# and project files, and — unlike every other location — it cannot be excluded by
# `claudeMdExcludes` at any settings layer. A project CLAUDE.md still loads and is
# still read LATER, so a repository can add to the house rules; it just cannot
# make them not apply.
#
# Written here rather than by the seed because the seed runs as uid 1000 and /etc
# is root's. Baking also means the agent cannot rewrite the rules it runs under.
COPY AGENTS.md /opt/proveo/AGENTS.md
COPY AGENTS.md /etc/claude-code/CLAUDE.md
COPY --chown=${USER_NAME}:${USER_NAME} packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY --chown=${USER_NAME}:${USER_NAME} packages/lib/proveo-seed /usr/local/bin/proveo-seed
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Default to the non-root build user; the wrapper overrides the runtime uid with
# `--user $(id -u):$(id -g)` (see ensure_runtime_user for arbitrary-uid handling).
USER ${USER_NAME}
WORKDIR /app

# claudecode-specific env; the common floor (npm/rlimit/corepack) is in proveo/base.
# NODE_ENV is deliberately NOT baked here: it would make every `npm`/`pnpm install`
# the agent runs in the workspace skip devDependencies (tsc, eslint, test runners),
# which are exactly the tools the agent needs. Leave it undefined and let the
# project's own .env set it if it wants one (load_env exports .env with `set -a`).
ENV YAMA_PTRACE_SCOPE=1

# Add security labels
LABEL security.non-root=true \
      security.hardened=true \
      security.version="1.0"

# Health check
HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
  CMD test -f /home/claude/.claude.json && test -f /home/claude/.claude/settings.local.json || exit 1

# Security: Use dumb-init for proper signal handling and process reaping
ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
