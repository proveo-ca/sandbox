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

RUN apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends \
    wget python3 python3-pip \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden /usr /opt

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

# Setup secure workspace with proper permissions
RUN mkdir -p /workspace/input \
    && mkdir -p /workspace/output \
    && mkdir -p /workspace/data \
    && mkdir -p /workspace/temp \
    && mkdir -p /workspace/mcp-servers \
    && mkdir -p /workspace/.claude \
    && mkdir -p /home/${USER_NAME}/.claude \
    && chown -R ${USER_NAME}:${USER_NAME} /workspace /home/${USER_NAME}/.claude \
    && chmod 755 /workspace \
    && chmod 750 /workspace/input \
    && chmod 750 /workspace/data \
    && chmod 755 /workspace/output \
    && chmod 755 /workspace/temp \
    && chmod 755 /workspace/mcp-servers

# Copy Claude configuration directly into final locations
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/claude-config.json /home/${USER_NAME}/.claude.json
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/settings.local.json /home/${USER_NAME}/.claude/settings.local.json
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/settings.local.json /workspace/.claude/settings.local.json
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/proveo-lib/ /opt/proveo/lib/
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/defaults/ /opt/claudecode/defaults/

# Copy shared entrypoint
COPY --chown=${USER_NAME}:${USER_NAME} packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Default to the non-root build user; the wrapper overrides the runtime uid with
# `--user $(id -u):$(id -g)` (see ensure_runtime_user for arbitrary-uid handling).
USER ${USER_NAME}
WORKDIR /workspace

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
