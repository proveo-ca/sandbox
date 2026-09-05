ARG BASE_IMAGE=proveo/base-node-lsp:latest

FROM ${BASE_IMAGE}

ARG DEBIAN_FRONTEND=noninteractive

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
    && /usr/local/sbin/proveo-harden

# SPEC: _spec/_plans/image-size-reduction.puml

RUN groupadd -f docker \
 && usermod -aG docker "${USER_NAME}" \
 && usermod -aG docker agent
LABEL com.docker.sandboxes.start-docker="true"

# SPEC: _spec/_devops/agent-version-pin.puml
ARG OPENCODE_VERSION
RUN test -n "${OPENCODE_VERSION}" \
      || { echo "OPENCODE_VERSION build-arg is required — defs/opencode/build.sh resolves it" >&2; exit 1; } \
    && npm install -g "opencode-ai@${OPENCODE_VERSION}" && npm cache clean --force \
    && test "$(opencode --version)" = "${OPENCODE_VERSION}"

RUN mkdir -p /workspace \
             /home/agent/.config/opencode \
             /home/agent/.local/share/opencode \
             /opt/opencode/defaults \
    && chown -R ${USER_NAME}:${USER_NAME} /workspace /home/agent

COPY defs/opencode/defaults/ /opt/opencode/defaults/
COPY defs/subagents/ /opt/proveo/subagents/
COPY defs/bridges/ /opt/proveo/bridges/
COPY defs/opencode/sample_opencode.json /opt/opencode/sample_opencode.json
COPY defs/lib/ /opt/proveo/lib/

COPY AGENTS.md /opt/proveo/AGENTS.md
COPY packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY --chmod=0755 defs/opencode/entrypoint.sh /entrypoint.sh

USER ${USER_NAME}
WORKDIR /workspace

ENV OPENCODE_AUTO_UPDATE=false \
    PATH="/usr/local/bin:${PATH}"

LABEL security.non-root=true \
      security.hardened=true \
      security.version="1.0" \
      proveo.agent="opencode-ai" \
      proveo.agent.version="${OPENCODE_VERSION}"

HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
  CMD opencode --version >/dev/null 2>&1 || exit 1

ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
