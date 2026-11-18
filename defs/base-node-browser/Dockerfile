# SPEC: _spec/components.puml, _spec/_devops/image-lineage-and-publish.puml, _spec/defs/browser-layer.puml
ARG BASE_IMAGE=proveo/base-node:latest
FROM ${BASE_IMAGE}
USER root

ARG DEBIAN_FRONTEND=noninteractive
ARG USER_NAME=root
ARG PLAYWRIGHT_VERSION=1.61.0

ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright

# Playwright 1.61 ubuntu26.04 chromium libraries.
RUN --mount=type=cache,id=proveo-base-node-browser-npm,target=/root/.npm,sharing=locked \
    --mount=type=cache,id=proveo-base-node-browser-apt-cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,id=proveo-base-node-browser-apt-lists,target=/var/lib/apt,sharing=locked \
    apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends \
       libasound2t64 \
       libatk-bridge2.0-0t64 \
       libatk1.0-0t64 \
       libatspi2.0-0t64 \
       libcairo2 \
       libcups2t64 \
       libdbus-1-3 \
       libdrm2 \
       libgbm1 \
       libglib2.0-0t64 \
       libnspr4 \
       libnss3 \
       libpango-1.0-0 \
       libx11-6 \
       libxcb1 \
       libxcomposite1 \
       libxdamage1 \
       libxext6 \
       libxfixes3 \
       libxkbcommon0 \
       libxrandr2 \
    && npm install -g --no-optional "playwright@${PLAYWRIGHT_VERSION}" \
    && playwright install --no-shell chromium \
    && ! ls -d "$PLAYWRIGHT_BROWSERS_PATH"/chromium_headless_shell-* >/dev/null 2>&1 \
    && chrome_bin="$(find "$PLAYWRIGHT_BROWSERS_PATH" -maxdepth 3 -type f -name chrome -path '*/chromium-*/*' | head -n1)" \
    && test -n "$chrome_bin" \
    && missing="$(ldd "$chrome_bin" | grep 'not found' || true)" \
    && test -z "$missing" || { printf '%s\n' "$missing" >&2; exit 1; } \
    && chmod -R a+rX "$PLAYWRIGHT_BROWSERS_PATH" \
    && rm -rf /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden /usr /opt

# SPEC: _spec/defs/browser-layer.puml
ENV AGENT_BROWSER_EXECUTABLE_PATH=/opt/proveo/chromium/chrome
RUN chrome_dir="$(dirname "$(find "$PLAYWRIGHT_BROWSERS_PATH" -maxdepth 3 -type f -name chrome -path '*/chromium-*/*' | head -n1)")" \
    && test -n "$chrome_dir" && test -x "$chrome_dir/chrome" \
    && mkdir -p /opt/proveo && ln -sfn "$chrome_dir" /opt/proveo/chromium \
    && test -x "$AGENT_BROWSER_EXECUTABLE_PATH"

USER ${USER_NAME}

LABEL security.hardened=true \
      org.opencontainers.image.title="proveo harness base (node + Playwright/Chromium + agent-browser)"
