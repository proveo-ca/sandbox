ARG BASE_IMAGE=proveo/base:latest

# proveo-entrypoint is inherited from proveo/base (built once in defs/base).
FROM ${BASE_IMAGE}

ARG USER_ID=1000
ARG USER_NAME=perplexity
# Official installer; override to pin a mirrored copy.
ARG PPLX_INSTALL_URL=https://github.com/perplexityai/perplexity-cli/releases/latest/download/install.sh

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

# Install pplx system-wide under a root-owned prefix (same immutability pattern
# as cursor). The installer writes to $HOME / PPLX_INSTALL_PATH; point both at
# /opt/pplx-dist so the agent uid cannot tamper with the binary.
RUN mkdir -p /opt/pplx-dist/bin \
    && PPLX_INSTALL_PATH=/opt/pplx-dist/bin/pplx HOME=/opt/pplx-dist \
       bash -c "curl -fsSL ${PPLX_INSTALL_URL} | sh" \
    && ln -sf /opt/pplx-dist/bin/pplx /usr/local/bin/pplx \
    && pplx --version \
    && /usr/local/sbin/proveo-harden

RUN mkdir -p /app \
             /home/${USER_NAME}/.config/pplx \
             /home/${USER_NAME}/.config/perplexity \
    && chown -R ${USER_NAME}:${USER_NAME} /app /home/${USER_NAME}

COPY defs/lib/ /opt/proveo/lib/

COPY packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY defs/perplexity/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

USER ${USER_NAME}
WORKDIR /app

ENV PATH="/usr/local/bin:${PATH}"

LABEL security.non-root=true \
      security.hardened=true \
      security.version="1.0"

HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
  CMD pplx --version >/dev/null 2>&1 || exit 1

ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
