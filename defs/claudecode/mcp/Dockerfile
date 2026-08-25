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
# user is `agent` with HOME=/home/agent: their setup.install chowns agent:agent and
# writes /home/agent/.claude/settings.json. proveo's images use uid 1000 under a
# harness-specific name, so both of those fail on a stock image and the sandbox
# never starts. An alias for the SAME uid, plus a home symlink, lets the inherited
# setup run exactly as authored without proveo's own paths moving.
RUN if ! getent group agent >/dev/null; then groupadd -o -g "${USER_ID}" agent; fi \
 && if ! getent passwd agent >/dev/null; then \
      useradd -o -u "${USER_ID}" -g "${USER_ID}" -M -d "/home/${USER_NAME}" -s /bin/bash agent; \
    fi \
 && { [ -e /home/agent ] || ln -s "/home/${USER_NAME}" /home/agent; }

RUN apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends \
    wget python3 python3-pip \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden /usr /opt

# Static docker client — the CLI the agent needs to reach the daemon the sandbox
# backend gives it (harness.manifest: docker: sbx). Same layer as cursor,
# opencode and cecli; keep the version in step with them.
RUN DOCKER_VERSION=26.1.4 \
    && case "$(uname -m)" in \
         x86_64|amd64) DOCKER_ARCH=x86_64 ;; \
         aarch64|arm64) DOCKER_ARCH=aarch64 ;; \
         *) echo "unsupported architecture for docker static client: $(uname -m)" >&2; exit 1 ;; \
       esac \
    && curl -fsSL "https://download.docker.com/linux/static/stable/${DOCKER_ARCH}/docker-${DOCKER_VERSION}.tgz" -o /tmp/docker.tgz \
    && tar -xzf /tmp/docker.tgz -C /tmp \
    && mv /tmp/docker/docker /usr/local/bin/docker \
    && rm -rf /tmp/docker /tmp/docker.tgz

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
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/claude-config.json /home/${USER_NAME}/.claude.json
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/settings.local.json /home/${USER_NAME}/.claude/settings.local.json
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/settings.local.json /app/.claude/settings.local.json
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/proveo-lib/ /opt/proveo/lib/
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/defaults/ /opt/claudecode/defaults/
COPY --chown=${USER_NAME}:${USER_NAME} defs/subagents/ /opt/proveo/subagents/
COPY --chown=${USER_NAME}:${USER_NAME} defs/bridges/ /opt/proveo/bridges/

# Copy shared entrypoint
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
