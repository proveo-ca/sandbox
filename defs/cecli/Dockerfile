ARG BASE_IMAGE=proveo/base:latest

FROM ${BASE_IMAGE}

ARG USER_ID=1000
ARG USER_NAME=cecli

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

# SPEC: _spec/_plans/image-size-reduction.puml
ENV PYTHONPYCACHEPREFIX=/tmp/proveo-pycache \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    CECLI_HOME=/app/.cecli

# SPEC: _spec/_plans/image-size-reduction.puml
RUN apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends \
    python3 \
    python3-pip \
    python3-venv \
    iptables \
    less \
  && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
  && /usr/local/sbin/proveo-harden /usr /opt

# SPEC: _spec/_devops/agent-version-pin.puml
ARG CECLI_VERSION
RUN test -n "${CECLI_VERSION}" \
      || { echo "CECLI_VERSION build-arg is required — defs/cecli/build.sh resolves it" >&2; exit 1; } \
  && python3 -m venv /opt/cecli \
  && /opt/cecli/bin/pip install --no-compile --upgrade pip setuptools wheel \
  && /opt/cecli/bin/pip install --no-compile "cecli-dev==${CECLI_VERSION}" boto3 botocore \
  && /opt/cecli/bin/pip show cecli-dev | grep -qx "Version: ${CECLI_VERSION}" \
  && rm -rf /root/.cache /tmp/* /var/tmp/* \
  && /usr/local/sbin/proveo-harden /usr /opt

ENV PATH="/opt/cecli/bin:${PATH}"

# SPEC: _spec/_devops/agent-version-pin.puml, _spec/defs/cecli/cecli-paradigm.puml
ARG SERENA_VERSION
RUN test -n "${SERENA_VERSION}" \
      || { echo "SERENA_VERSION build-arg is required — defs/cecli/build.sh resolves it" >&2; exit 1; } \
  && python3 -m venv /opt/serena \
  && /opt/serena/bin/pip install --no-cache-dir --no-compile "serena-agent==${SERENA_VERSION}" \
  && /opt/serena/bin/pip show serena-agent | grep -qx "Version: ${SERENA_VERSION}" \
  && ln -sf /opt/serena/bin/serena /usr/local/bin/serena \
  && rm -rf /root/.cache /tmp/* /var/tmp/* \
  && /usr/local/sbin/proveo-harden /usr /opt

# SPEC: _spec/_plans/image-size-reduction.puml

RUN groupadd -f docker \
 && usermod -aG docker "${USER_NAME}" \
 && usermod -aG docker agent
LABEL com.docker.sandboxes.start-docker="true"

WORKDIR /app

COPY packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY --chmod=0755 defs/cecli/entrypoint.sh /usr/local/bin/cecli-entrypoint
COPY defs/cecli/defaults/ /opt/cecli/defaults/
COPY defs/subagents/ /opt/proveo/subagents/
COPY defs/bridges/ /opt/proveo/bridges/
COPY defs/cecli/proveo-lib/ /opt/proveo/lib/
RUN chown "${USER_NAME}:${USER_NAME}" /app

USER ${USER_NAME}

LABEL proveo.agent="cecli-dev" \
      proveo.agent.version="${CECLI_VERSION}"

ENTRYPOINT ["dumb-init", "--", "cecli-entrypoint"]
CMD ["cecli"]
