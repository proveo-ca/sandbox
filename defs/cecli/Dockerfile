# cecli = the aider fork (cecli-dev) in a Python venv on proveo/base. No Node, no
# browsers (deduped from the old MCR playwright/python variant).
ARG BASE_IMAGE=proveo/base:latest

# proveo-entrypoint is inherited from proveo/base (built once in defs/base).
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

# cecli-specific env; the common floor lives in proveo/base
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    CECLI_HOME=/app/.cecli

RUN apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends \
    python3 \
    python3-pip \
    python3-venv \
    build-essential \
    less \
  && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
  && /usr/local/sbin/proveo-harden /usr /opt

# cecli-dev is pinned by version, never bare: BuildKit caches this layer by its
# text, so an unpinned install re-runs only when something ABOVE it changes, and a
# warm cache keeps last month's agent with nothing in the build saying so. build.sh
# resolves the current PyPI release (proveo_agent_version) and passes it in, which
# moves the cache key exactly when upstream moves; the version check makes the pin
# a fact about the image. No default on purpose — a bare `docker build` must fail
# here, not bake whatever is newest.
# SPEC: _spec/_devops/agent-version-pin.puml
ARG CECLI_VERSION
RUN test -n "${CECLI_VERSION}" \
      || { echo "CECLI_VERSION build-arg is required — defs/cecli/build.sh resolves it" >&2; exit 1; } \
  && python3 -m venv /opt/cecli \
  && /opt/cecli/bin/pip install --upgrade pip setuptools wheel \
  && /opt/cecli/bin/pip install "cecli-dev==${CECLI_VERSION}" boto3 botocore \
  && /opt/cecli/bin/pip show cecli-dev | grep -qx "Version: ${CECLI_VERSION}" \
  && rm -rf /root/.cache /tmp/* /var/tmp/* \
  && /usr/local/sbin/proveo-harden /usr /opt

ENV PATH="/opt/cecli/bin:${PATH}"

RUN DOCKER_VERSION=29.7.2 \
    && case "$(uname -m)" in \
         x86_64|amd64) DOCKER_ARCH=x86_64 ;; \
         aarch64|arm64) DOCKER_ARCH=aarch64 ;; \
         *) echo "unsupported architecture for docker static client: $(uname -m)" >&2; exit 1 ;; \
       esac \
    && curl -fsSL "https://download.docker.com/linux/static/stable/${DOCKER_ARCH}/docker-${DOCKER_VERSION}.tgz" -o /tmp/docker.tgz \
    && tar -xzf /tmp/docker.tgz -C /tmp \
    && mv /tmp/docker/docker /usr/local/bin/docker \
    && rm -rf /tmp/docker /tmp/docker.tgz

WORKDIR /app

COPY packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY defs/cecli/entrypoint.sh /usr/local/bin/cecli-entrypoint
COPY defs/cecli/defaults/ /opt/cecli/defaults/
COPY defs/subagents/ /opt/proveo/subagents/
COPY defs/bridges/ /opt/proveo/bridges/
COPY defs/cecli/proveo-lib/ /opt/proveo/lib/
RUN chmod +x /usr/local/bin/cecli-entrypoint \
  && chown "${USER_NAME}:${USER_NAME}" /app

# Default to the non-root build user; the wrapper overrides the runtime uid with
# `--user $(id -u):$(id -g)` (see ensure_runtime_user for arbitrary-uid handling).
USER ${USER_NAME}

# What agent the image carries, readable without running it:
#   docker image inspect -f '{{index .Config.Labels "proveo.agent.version"}}' proveo/cecli:local
LABEL proveo.agent="cecli-dev" \
      proveo.agent.version="${CECLI_VERSION}"

ENTRYPOINT ["dumb-init", "--", "cecli-entrypoint"]
CMD ["cecli"]
