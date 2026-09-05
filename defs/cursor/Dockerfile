# SPEC: _spec/defs/cursor/cursor-topology.puml, _spec/defs/cursor/cursor-paradigm.puml, _spec/_devops/image-lineage-and-publish.puml
ARG BASE_IMAGE=proveo/base:latest

FROM golang:1.26-trixie AS lsp-builder
ENV CGO_ENABLED=0
RUN go install github.com/isaacphi/mcp-language-server@latest \
    && go install golang.org/x/tools/gopls@latest

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

# SPEC: _spec/_plans/image-size-reduction.puml

RUN groupadd -f docker \
 && usermod -aG docker "${USER_NAME}" \
 && usermod -aG docker agent
LABEL com.docker.sandboxes.start-docker="true"

COPY --from=lsp-builder /go/bin/mcp-language-server /go/bin/gopls /usr/local/bin/

# SPEC: _spec/_devops/agent-version-pin.puml
ARG CURSOR_AGENT_VERSION
RUN test -n "${CURSOR_AGENT_VERSION}" \
      || { echo "CURSOR_AGENT_VERSION build-arg is required — defs/cursor/build.sh resolves it" >&2; exit 1; } \
    && HOME=/opt/cursor-dist bash -c "curl -fsS ${CURSOR_INSTALL_URL} | bash" \
    && test -d "/opt/cursor-dist/.local/share/cursor-agent/versions/${CURSOR_AGENT_VERSION}" \
    && ln -s /opt/cursor-dist/.local/bin/agent /usr/local/bin/agent \
    && ln -s /opt/cursor-dist/.local/bin/cursor-agent /usr/local/bin/cursor-agent \
    && agent --version

RUN mkdir -p /app \
             /home/agent/.cursor \
             /opt/cursor/defaults \
    && chown -R ${USER_NAME}:${USER_NAME} /app /home/agent

COPY defs/cursor/defaults/ /opt/cursor/defaults/
COPY defs/subagents/ /opt/proveo/subagents/
COPY defs/bridges/ /opt/proveo/bridges/
RUN chmod +x /opt/cursor/defaults/hooks/*.sh

RUN mkdir -p /etc/cursor \
    && cp /opt/cursor/defaults/hooks.json /etc/cursor/hooks.json \
    && chmod 644 /etc/cursor/hooks.json

COPY defs/lib/ /opt/proveo/lib/

COPY packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY --chmod=0755 packages/lib/proveo-seed /usr/local/bin/proveo-seed
COPY --chmod=0755 defs/cursor/entrypoint.sh /entrypoint.sh

USER ${USER_NAME}
WORKDIR /app

ENV NO_OPEN_BROWSER=1 \
    PATH="/usr/local/bin:${PATH}"

LABEL security.non-root=true \
      security.hardened=true \
      security.version="1.0" \
      proveo.agent="cursor-agent" \
      proveo.agent.version="${CURSOR_AGENT_VERSION}"

HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
  CMD agent --version >/dev/null 2>&1 || exit 1

ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
