ARG BASE_IMAGE=proveo/base-node-lsp:latest

FROM ${BASE_IMAGE}

ARG DEBIAN_FRONTEND=noninteractive

ARG USER_ID=1000
ARG USER_NAME=claude

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

# SPEC: _spec/_plans/image-size-reduction.puml

RUN groupadd -f docker \
 && usermod -aG docker "${USER_NAME}" \
 && usermod -aG docker agent
LABEL com.docker.sandboxes.start-docker="true"

RUN pip3 install --no-cache-dir --break-system-packages \
    requests \
    python-dotenv

# SPEC: _spec/_plans/image-size-reduction.puml

# SPEC: _spec/_devops/agent-version-pin.puml
ARG CLAUDE_CODE_VERSION
RUN test -n "${CLAUDE_CODE_VERSION}" \
      || { echo "CLAUDE_CODE_VERSION build-arg is required — defs/claudecode/build.sh resolves it" >&2; exit 1; } \
    && npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" && npm cache clean --force \
    && claude --version | grep -qF "${CLAUDE_CODE_VERSION}"

# SPEC: _spec/defs/claudecode/lsp-plugins-seed.puml
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

COPY defs/claudecode/mcp/themes/ /opt/proveo/themes/
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/claude-config.json /home/${USER_NAME}/.claude.json
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/settings.local.json /home/${USER_NAME}/.claude/settings.local.json
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/settings.local.json /app/.claude/settings.local.json
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/proveo-lib/ /opt/proveo/lib/
COPY --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/defaults/ /opt/claudecode/defaults/
COPY --chown=${USER_NAME}:${USER_NAME} defs/subagents/ /opt/proveo/subagents/
COPY --chown=${USER_NAME}:${USER_NAME} defs/bridges/ /opt/proveo/bridges/

COPY AGENTS.md /opt/proveo/AGENTS.md
COPY AGENTS.md /etc/claude-code/CLAUDE.md
COPY --chown=${USER_NAME}:${USER_NAME} packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY --chmod=0755 --chown=${USER_NAME}:${USER_NAME} packages/lib/proveo-seed /usr/local/bin/proveo-seed
COPY --chmod=0755 --chown=${USER_NAME}:${USER_NAME} defs/claudecode/mcp/entrypoint.sh /entrypoint.sh

USER ${USER_NAME}
WORKDIR /app

LABEL security.non-root=true \
      security.hardened=true \
      security.version="1.0" \
      proveo.agent="@anthropic-ai/claude-code" \
      proveo.agent.version="${CLAUDE_CODE_VERSION}"

HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
  CMD test -f /home/agent/.claude.json && test -f /home/agent/.claude/settings.local.json || exit 1

ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
