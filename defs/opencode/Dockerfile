ARG BASE_IMAGE=proveo/base-node-lsp:latest

# proveo-entrypoint (shared in-container prelude) is inherited from
# proveo/base-node — built once in defs/base, no per-harness rebuild.
FROM ${BASE_IMAGE}

ARG USER_ID=1000
ARG USER_NAME=opencode

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
# the kit mounts block volumes underneath it. /home/agent must therefore be a REAL
# directory and the actual home — a mount target cannot resolve through a symlink,
# so containerd materialises it as a fresh directory and shadows the link.
#
# So the REAL home is /home/agent and the harness name is the alias. The same
# reversal cursor needed; opencode joins it because `docker: sbx` puts it on the
# same backend, under sbx's own `opencode` agent.
RUN usermod -d /home/agent -m "${USER_NAME}" \
 && if ! getent group agent >/dev/null; then groupadd -o -g "${USER_ID}" agent; fi \
 && if ! getent passwd agent >/dev/null; then \
      useradd -o -u "${USER_ID}" -g "${USER_ID}" -M -d /home/agent -s /bin/bash agent; \
    fi \
 && { [ -e "/home/${USER_NAME}" ] || ln -s /home/agent "/home/${USER_NAME}"; } \
 && chown -R "${USER_ID}:${USER_ID}" /home/agent

# Harness extras on top of proveo/base (git/gh/curl/python3/dumb-init/pnpm and
# the hardening pass live there); re-run the harden pass after installing.
# plantuml + plantuml-lsp now come from proveo/base-node-lsp, not from here.
# iptables is the daemon's, not opencode's: sbx's per-sandbox daemon builds its own
# NAT chain at start and cannot without it. It stays even though this image no
# longer ships dockerd — a NAT chain needs iptables whoever builds it.
RUN apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends \
    xclip \
    iptables \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden

# No docker binaries here. The whole static tarball — docker, dockerd, containerd,
# containerd-shim-runc-v2, ctr, runc, docker-proxy, docker-init, 210 MB measured —
# was removed when sbx became the only backend that hands this harness a daemon:
# every one of those binaries overlaps the sandbox runtime that owns it.
#
# The LABEL below, the `docker` group and `iptables` are NOT overlaps and stay:
# sbx's own switch, the group on a root:docker socket, and the NAT chain's
# dependency. The assumption this rests on (sbx supplies the client AND the
# daemon), the evidence in this tree pointing the other way, and the exact symptom
# if it is wrong: _spec/_plans/image-size-reduction.puml.
# SPEC: _spec/_plans/image-size-reduction.puml

# ── Docker-in-sandbox ───────────────────────────────────────────────────────
#
# sbx decides whether to start the sandbox's own daemon from an IMAGE LABEL —
# `com.docker.sandboxes.start-docker`. Without it sbx records `dind: false` at
# container creation and never starts one, so `docker version` answers "Cannot
# connect to the Docker daemon" beside Compose files the agent can plainly read.
# The socket is created root:docker, so the runtime user needs the group.
RUN groupadd -f docker \
 && usermod -aG docker "${USER_NAME}" \
 && usermod -aG docker agent
LABEL com.docker.sandboxes.start-docker="true"

# The workspace language servers now live in proveo/base-node-lsp (shared with
# claudecode). Only the opencode agent itself is installed here.
#
# Pinned by version, never `@latest`: BuildKit caches this layer by its text, so
# an `@latest` line re-runs only when something ABOVE it changes, and a warm cache
# keeps last month's agent with nothing in the build saying so. build.sh resolves
# the current release (proveo_agent_version) and passes it in, which moves the
# cache key exactly when upstream moves; the version check makes the pin a fact
# about the image rather than a request to npm. No default on purpose — a bare
# `docker build` must fail here instead of silently baking whatever is newest.
# SPEC: _spec/_devops/agent-version-pin.puml
ARG OPENCODE_VERSION
RUN test -n "${OPENCODE_VERSION}" \
      || { echo "OPENCODE_VERSION build-arg is required — defs/opencode/build.sh resolves it" >&2; exit 1; } \
    && npm install -g "opencode-ai@${OPENCODE_VERSION}" && npm cache clean --force \
    && test "$(opencode --version)" = "${OPENCODE_VERSION}"

# The chown targets /home/agent, NOT /home/${USER_NAME}.
#
# /home/${USER_NAME} is the SYMLINK created above. `mkdir -p` follows it, so these
# directories are created inside the real home as root; `chown -R` does not follow
# it, so pointing the chown at the link changed the link's own ownership and left
# every directory under it root-owned. The agent then died at its first write:
#   mkdir: cannot create directory '/home/agent/.config/opencode/agents':
#   Permission denied
# — after a build that reported success, and with `ls -l /home/opencode` showing
# 1000:1000 because that IS the link. cursor chowns /home/agent and claudecode
# chowns /home/${USER_NAME}/.claude; both end at a real directory, which is why
# opencode was the only harness that failed the detached smoke.
# Guarded by internal/contract TestNoDockerfileChownsTheHomeSymlink.
RUN mkdir -p /workspace \
             /home/agent/.config/opencode \
             /home/agent/.local/share/opencode \
             /opt/opencode/defaults \
    && chown -R ${USER_NAME}:${USER_NAME} /workspace /home/agent

# Pre-baked defaults (config + subagents). Seeded into ~/.config/opencode on first run
# by entrypoint.sh, or re-seeded when OPENCODE_RESEED=1.
COPY defs/opencode/defaults/ /opt/opencode/defaults/
COPY defs/subagents/ /opt/proveo/subagents/
COPY defs/bridges/ /opt/proveo/bridges/
COPY defs/opencode/sample_opencode.json /opt/opencode/sample_opencode.json
COPY defs/lib/ /opt/proveo/lib/

# proveo's own conventions, installed as the harness's USER-level instructions
# by proveo_compose_house_rules. Baked in rather than mounted so it is present
# on both backends and cannot be edited from inside the sandbox.
COPY AGENTS.md /opt/proveo/AGENTS.md
COPY packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY defs/opencode/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Default to the non-root build user; the wrapper overrides the runtime uid with
# `--user $(id -u):$(id -g)` (see ensure_runtime_user for arbitrary-uid handling).
USER ${USER_NAME}
WORKDIR /workspace

# opencode-specific env; the common floor lives in proveo/base
ENV OPENCODE_AUTO_UPDATE=false \
    PATH="/usr/local/bin:${PATH}"

# proveo.agent.* is what the image carries, readable without running it:
#   docker image inspect -f '{{index .Config.Labels "proveo.agent.version"}}' proveo/opencode:local
LABEL security.non-root=true \
      security.hardened=true \
      security.version="1.0" \
      proveo.agent="opencode-ai" \
      proveo.agent.version="${OPENCODE_VERSION}"

HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
  CMD opencode --version >/dev/null 2>&1 || exit 1

ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
