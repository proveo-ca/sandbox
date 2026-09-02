# SPEC: _spec/defs/cursor/cursor-topology.puml, _spec/defs/cursor/cursor-paradigm.puml, _spec/_devops/image-lineage-and-publish.puml
ARG BASE_IMAGE=proveo/base:latest

# ── LSP tooling builder ────────────────────────────────────
FROM golang:1.26-trixie AS lsp-builder
ENV CGO_ENABLED=0
RUN go install github.com/isaacphi/mcp-language-server@latest \
    && go install golang.org/x/tools/gopls@latest

# proveo-entrypoint is inherited from proveo/base (built once in defs/base).
FROM ${BASE_IMAGE}

ARG USER_ID=1000
ARG USER_NAME=cursor
ARG CURSOR_INSTALL_URL=https://cursor.com/install

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
    iptables \
    unzip \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden

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



COPY --from=lsp-builder /go/bin/mcp-language-server /go/bin/gopls /usr/local/bin/

# cursor.com/install hardcodes the build it unpacks and takes no version argument,
# so the pin works the other way round: build.sh reads the release out of the
# script (proveo_agent_version cursor) and this layer VERIFIES it installed that
# release — the versions/<release> directory is the installer's own layout. Using
# the arg here is what moves BuildKit's cache key when the installer moves; a
# `curl | bash` line alone never changes, and a warm cache keeps last month's
# agent with nothing in the build saying so. No default on purpose.
# SPEC: _spec/_devops/agent-version-pin.puml
ARG CURSOR_AGENT_VERSION
RUN test -n "${CURSOR_AGENT_VERSION}" \
      || { echo "CURSOR_AGENT_VERSION build-arg is required — defs/cursor/build.sh resolves it" >&2; exit 1; } \
    && HOME=/opt/cursor-dist bash -c "curl -fsS ${CURSOR_INSTALL_URL} | bash" \
    && test -d "/opt/cursor-dist/.local/share/cursor-agent/versions/${CURSOR_AGENT_VERSION}" \
    && ln -s /opt/cursor-dist/.local/bin/agent /usr/local/bin/agent \
    && ln -s /opt/cursor-dist/.local/bin/cursor-agent /usr/local/bin/cursor-agent \
    && agent --version

# /home/agent, not /home/${USER_NAME}: the latter is now a SYMLINK to it, and
# `chown -R` given a symlink as its ARGUMENT chowns the link and stops — it does
# not recurse into the target without -L. Left as-is, `.cursor` was created root
# owned through the link, the seed's `mkdir .cursor/agents` hit Permission denied,
# and the entrypoint's `set -e` killed the container before the agent ever ran.
# sbx reported that as "failed to run sandbox container" with no cause.
RUN mkdir -p /app \
             /home/agent/.cursor \
             /opt/cursor/defaults \
    && chown -R ${USER_NAME}:${USER_NAME} /app /home/agent

COPY defs/cursor/defaults/ /opt/cursor/defaults/
COPY defs/subagents/ /opt/proveo/subagents/
COPY defs/bridges/ /opt/proveo/bridges/
RUN chmod +x /opt/cursor/defaults/hooks/*.sh

# Enterprise hook layer: root-owned, highest precedence (enterprise > team >
# project > user), so the sandboxed agent can neither edit nor out-rank it.
RUN mkdir -p /etc/cursor \
    && cp /opt/cursor/defaults/hooks.json /etc/cursor/hooks.json \
    && chmod 644 /etc/cursor/hooks.json

COPY defs/lib/ /opt/proveo/lib/

COPY packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY packages/lib/proveo-seed /usr/local/bin/proveo-seed
COPY defs/cursor/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Default to the non-root build user; the wrapper overrides the runtime uid with
# `--user $(id -u):$(id -g)` (see ensure_runtime_user for arbitrary-uid handling).
USER ${USER_NAME}
WORKDIR /app

# cursor-specific env; the common floor lives in proveo/base
ENV NO_OPEN_BROWSER=1 \
    PATH="/usr/local/bin:${PATH}"

# proveo.agent.* is what the image carries, readable without running it:
#   docker image inspect -f '{{index .Config.Labels "proveo.agent.version"}}' proveo/cursor:local
LABEL security.non-root=true \
      security.hardened=true \
      security.version="1.0" \
      proveo.agent="cursor-agent" \
      proveo.agent.version="${CURSOR_AGENT_VERSION}"

HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
  CMD agent --version >/dev/null 2>&1 || exit 1

ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
