# SPEC: _spec/defs/codex/codex-topology.puml, _spec/defs/codex/codex-paradigm.puml, _spec/_devops/image-lineage-and-publish.puml
ARG BASE_IMAGE=proveo/base-node-lsp:latest

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

# sbx's built-in agent kits are authored against their own template, whose runtime
# user is `agent` with HOME=/home/agent: their setup.install chowns agent:agent,
# and the kit mounts block volumes UNDER /home/agent.
#
# /home/agent must therefore be a REAL directory and the actual home. An alias plus
# a home SYMLINK is not enough: a mount target cannot resolve through a symlink, so
# containerd materialises /home/agent as a fresh directory, shadowing the link. The
# volumes then land somewhere the agent never reads while $HOME points elsewhere —
# including the session state `codex resume` is read from.
#
# So the REAL home is /home/agent and the harness-specific name is the alias.
# /home/${USER_NAME} stays a symlink because image healthchecks and def tests read
# paths through it, and nothing ever mounts there.
RUN usermod -d /home/agent -m "${USER_NAME}" \
 && if ! getent group agent >/dev/null; then groupadd -o -g "${USER_ID}" agent; fi \
 && if ! getent passwd agent >/dev/null; then \
      useradd -o -u "${USER_ID}" -g "${USER_ID}" -M -d /home/agent -s /bin/bash agent; \
    fi \
 && { [ -e "/home/${USER_NAME}" ] || ln -s /home/agent "/home/${USER_NAME}"; } \
 && chown -R "${USER_ID}:${USER_ID}" /home/agent

RUN apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends \
    iptables \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden /usr /opt

# Static docker CLI only. sbx gives each sandbox its own Docker daemon, but it runs
# from the IMAGE and starts as root; shipping dockerd makes sbx ATTEMPT that start,
# and a daemon that fails to start blocks the whole sandbox. See the long-form
# record of the three failed attempts in defs/claudecode/mcp/Dockerfile — it is the
# same packaging and the same open sbx-side question.
#
# Keep the version in step with claudecode, cursor, opencode and cecli.
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
# sbx decides whether to start a daemon from an IMAGE LABEL —
# `com.docker.sandboxes.start-docker`, which its own templates carry. Without the
# label sbx records `dind: false` at container creation and never starts one.
RUN groupadd -f docker \
 && usermod -aG docker "${USER_NAME}" \
 && usermod -aG docker agent
LABEL com.docker.sandboxes.start-docker="true"

# Install the Codex CLI. The npm distribution ships the same native binary as the
# install script and is the pinnable one — a bare `curl | sh` resolves whatever
# release is current, which is not a thing an image can reproduce.
ARG CODEX_VERSION=latest
RUN npm install -g "@openai/codex@${CODEX_VERSION}" && npm cache clean --force \
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
COPY defs/bridges/ /opt/proveo/bridges/
COPY defs/lib/ /opt/proveo/lib/

# proveo's own conventions, installed as the harness's USER-level instructions by
# proveo_compose_house_rules ($CODEX_HOME/AGENTS.md — the documented global file).
#
# Codex has no managed-policy tier of its own, so unlike claudecode there is no
# /etc file that outranks the project's. What it does have is native AGENTS.md
# support: the repo's own AGENTS.md is read with no CLAUDE.md bridge, and it ranks
# ABOVE the global one — so the house rules add to a repo's rules, never replace
# them, and a project can still overrule the house.
COPY AGENTS.md /opt/proveo/AGENTS.md
COPY --chown=${USER_ID}:${USER_ID} packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY --chown=${USER_ID}:${USER_ID} packages/lib/proveo-seed /usr/local/bin/proveo-seed
COPY --chown=${USER_ID}:${USER_ID} defs/codex/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh /usr/local/bin/proveo-seed

# Default to the non-root build user; the wrapper overrides the runtime uid with
# `--user $(id -u):$(id -g)` (see ensure_runtime_user for arbitrary-uid handling).
USER ${USER_NAME}
WORKDIR /app

# codex-specific env; the common floor (npm/rlimit/corepack) is in proveo/base.
#
# NODE_ENV is deliberately NOT baked: it would make every `npm`/`pnpm install` the
# agent runs in the workspace skip devDependencies (tsc, eslint, test runners),
# which are exactly the tools the agent needs.
#
# CODEX_HOME is deliberately NOT baked. It defaults to $HOME/.codex, and $HOME is
# what proveo redirects: the manifest mounts the durable ~/.proveo/.codex at
# /proveo-home/.codex and sets HOME=/proveo-home. An absolute CODEX_HOME frozen
# here would point the CLI back at the IMAGE's home — past the mount — and every
# session, login and config write would be discarded with the container.
ENV YAMA_PTRACE_SCOPE=1 \
    PATH="/usr/local/bin:${PATH}"

LABEL security.non-root=true \
      security.hardened=true \
      security.version="1.0"

HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
  CMD codex --version >/dev/null 2>&1 || exit 1

# Security: dumb-init for proper signal handling and process reaping
ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
