# SPEC: _spec/defs/cursor/cursor-topology.puml, _spec/defs/cursor/cursor-paradigm.puml, _spec/_devops/image-lineage-and-publish.puml
ARG BASE_IMAGE=proveo/base:latest

# ── LSP tooling builder ────────────────────────────────────
FROM golang:1.26-bookworm AS lsp-builder
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
    unzip \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden

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

COPY --from=lsp-builder /go/bin/mcp-language-server /go/bin/gopls /usr/local/bin/

RUN HOME=/opt/cursor-dist bash -c "curl -fsS ${CURSOR_INSTALL_URL} | bash" \
    && ln -s /opt/cursor-dist/.local/bin/agent /usr/local/bin/agent \
    && ln -s /opt/cursor-dist/.local/bin/cursor-agent /usr/local/bin/cursor-agent \
    && agent --version

RUN mkdir -p /app \
             /home/${USER_NAME}/.cursor \
             /opt/cursor/defaults \
    && chown -R ${USER_NAME}:${USER_NAME} /app /home/${USER_NAME}

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

LABEL security.non-root=true \
      security.hardened=true \
      security.version="1.0"

HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
  CMD agent --version >/dev/null 2>&1 || exit 1

ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
