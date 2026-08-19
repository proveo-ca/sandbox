# SPEC: _spec/components.puml, _spec/_devops/image-lineage-and-publish.puml
ARG BASE_IMAGE=proveo/base-node-lsp:latest
FROM ${BASE_IMAGE}

ARG PLAYWRIGHT_VERSION=1.61.0

ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright

RUN npm install -g --no-optional "playwright@${PLAYWRIGHT_VERSION}" \
    && playwright install --with-deps chromium \
    && chmod -R a+rX "$PLAYWRIGHT_BROWSERS_PATH" \
    && npm cache clean --force \
    && rm -rf /tmp/* /var/tmp/* /var/lib/apt/lists/* \
    && /usr/local/sbin/proveo-harden /usr /opt

LABEL security.hardened=true \
      org.opencontainers.image.title="proveo harness base (node + LSP + Playwright/Chromium)"
