# SPEC: _spec/defs/codex/codex-topology.puml, _spec/defs/codex/codex-paradigm.puml, _spec/_devops/image-lineage-and-publish.puml
ARG BASE_IMAGE=proveo/base-node-lsp:latest

# mcp-language-server: carries the base's language servers to codex over MCP.
FROM golang:1.26-trixie AS lsp-builder
ARG MCP_LANGUAGE_SERVER_VERSION=v0.1.1
ENV CGO_ENABLED=0
RUN --mount=type=cache,id=proveo-codex-gomod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=proveo-codex-gocache,target=/root/.cache/go-build,sharing=locked \
    go install "github.com/isaacphi/mcp-language-server@${MCP_LANGUAGE_SERVER_VERSION}"

# proveo-entrypoint is inherited from proveo/base-node (built once in defs/base).
FROM ${BASE_IMAGE}

ARG USER_ID=1000
ARG USER_NAME=codex

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

# /home/agent is the REAL home (sbx mounts kit volumes under it); /home/${USER_NAME} is an alias.
RUN usermod -d /home/agent -m "${USER_NAME}" \
 && if ! getent group agent >/dev/null; then groupadd -o -g "${USER_ID}" agent; fi \
 && if ! getent passwd agent >/dev/null; then \
      useradd -o -u "${USER_ID}" -g "${USER_ID}" -M -d /home/agent -s /bin/bash agent; \
    fi \
 && { [ -e "/home/${USER_NAME}" ] || ln -s /home/agent "/home/${USER_NAME}"; } \
 && chown -R "${USER_ID}:${USER_ID}" /home/agent

# ── Docker-in-sandbox ───────────────────────────────────────────────────────
# sbx decides whether to start a daemon from an IMAGE LABEL —
# `com.docker.sandboxes.start-docker`, which its own templates carry. Without the
# label sbx records `dind: false` at container creation and never starts one.
RUN groupadd -f docker \
 && usermod -aG docker "${USER_NAME}" \
 && usermod -aG docker agent
LABEL com.docker.sandboxes.start-docker="true"

COPY --from=lsp-builder /go/bin/mcp-language-server /usr/local/bin/

# Install the Codex CLI. The npm distribution ships the same native binary as the
# install script and is the pinnable one — a bare `curl | sh` resolves whatever
# release is current, which is not a thing an image can reproduce.
ARG CODEX_VERSION=latest
RUN --mount=type=cache,id=proveo-codex-npm,target=/root/.npm,sharing=locked \
    npm install -g "@openai/codex@${CODEX_VERSION}" \
    && codex --version

# Secure workspace. The workspace root is /app, the same as every other def;
# /workspace/data survives because --data-dir mounts there for EVERY harness.
RUN mkdir -p /app \
    && mkdir -p /app/output \
    && mkdir -p /workspace/data \
    && mkdir -p /workspace/temp \
    && mkdir -p /home/agent/.codex \
    && mkdir -p /opt/codex/defaults \
    && chown -R ${USER_ID}:${USER_ID} /app /workspace /home/agent \
    && chmod 755 /workspace \
    && chmod 750 /app \
    && chmod 750 /workspace/data \
    && chmod 755 /app/output \
    && chmod 755 /workspace/temp

# Pre-baked defaults (config.toml). Seeded into $CODEX_HOME on first run by
# entrypoint.sh, or re-seeded when CODEX_RESEED=1.
COPY defs/codex/defaults/ /opt/codex/defaults/
COPY defs/subagents/ /opt/proveo/subagents/

# proveo house rules, installed as $CODEX_HOME/AGENTS.md (user level) by proveo_compose_house_rules.
COPY AGENTS.md /opt/proveo/AGENTS.md
COPY --chown=${USER_ID}:${USER_ID} packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY --chown=${USER_ID}:${USER_ID} packages/lib/proveo-seed /usr/local/bin/proveo-seed
COPY --chmod=0755 packages/lib/hooks/git-sync-turn.sh /opt/proveo/hooks/git-sync-turn.sh
COPY packages/lib/hooks/proveo-git-sync-turn.js /opt/proveo/hooks/proveo-git-sync-turn.js
COPY defs/codex/managed/requirements.toml /etc/codex/requirements.toml
COPY --chown=${USER_ID}:${USER_ID} defs/codex/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh /usr/local/bin/proveo-seed \
    && chmod 0755 /etc/codex && chmod 0644 /etc/codex/requirements.toml

# Default to the non-root build user; the wrapper overrides the runtime uid with
# `--user $(id -u):$(id -g)` (proveo-entrypoint prep adds the passwd entry for an arbitrary uid).
USER ${USER_NAME}
WORKDIR /app

# codex-specific env; the common floor (npm/rlimit/corepack) is in proveo/base.
# NODE_ENV and CODEX_HOME are deliberately not baked.
ENV YAMA_PTRACE_SCOPE=1 \
    PATH="/usr/local/bin:${PATH}"

LABEL security.non-root=true \
      security.hardened=true \
      security.version="1.0"

HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
  CMD codex --version >/dev/null 2>&1 || exit 1

# Security: dumb-init for proper signal handling and process reaping
ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
