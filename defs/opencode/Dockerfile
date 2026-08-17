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
RUN apt-get update && apt-get install -y --no-install-recommends \
    unzip xclip \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden

# Install lightweight docker static client (non-setuid).
# Resolve the tarball arch from the build platform so arm64 images get an
# arm64 (aarch64) binary instead of a non-runnable x86_64 one.
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

# The workspace language servers now live in proveo/base-node-lsp (shared with
# claudecode). Only the opencode agent itself is installed here.
RUN npm install -g opencode-ai@latest && npm cache clean --force

RUN mkdir -p /workspace \
             /home/${USER_NAME}/.config/opencode \
             /home/${USER_NAME}/.local/share/opencode \
             /opt/opencode/defaults \
    && chown -R ${USER_NAME}:${USER_NAME} /workspace /home/${USER_NAME}

# Pre-baked defaults (config + subagents). Seeded into ~/.config/opencode on first run
# by entrypoint.sh, or re-seeded when OPENCODE_RESEED=1.
COPY defs/opencode/defaults/ /opt/opencode/defaults/
COPY defs/opencode/sample_opencode.json /opt/opencode/sample_opencode.json
COPY defs/lib/ /opt/proveo/lib/

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

LABEL security.non-root=true \
      security.hardened=true \
      security.version="1.0"

HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
  CMD opencode --version >/dev/null 2>&1 || exit 1

ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
