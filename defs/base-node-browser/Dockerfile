# SPEC: _spec/components.puml, _spec/_devops/image-lineage-and-publish.puml, _spec/defs/browser-layer.puml
ARG BASE_IMAGE=proveo/base-node-lsp:latest
FROM ${BASE_IMAGE}

ARG DEBIAN_FRONTEND=noninteractive

ARG PLAYWRIGHT_VERSION=1.61.0

ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright

RUN npm install -g --no-optional "playwright@${PLAYWRIGHT_VERSION}" \
    && playwright install --with-deps --no-shell chromium \
    && ! ls -d "$PLAYWRIGHT_BROWSERS_PATH"/chromium_headless_shell-* >/dev/null 2>&1 \
    && chmod -R a+rX "$PLAYWRIGHT_BROWSERS_PATH" \
    && npm cache clean --force \
    && rm -rf /tmp/* /var/tmp/* /var/lib/apt/lists/* \
    && /usr/local/sbin/proveo-harden /usr /opt

# SPEC: _spec/defs/browser-layer.puml
ARG AGENT_BROWSER_VERSION=0.36.0
ARG AGENT_BROWSER_SHA256=858a753760034d73c6bc17dd895f91141ef03c3fcca978348b378e26d2d617e4
ENV AGENT_BROWSER_SKILLS_DIR=/opt/agent-browser/skill-data \
    AGENT_BROWSER_EXECUTABLE_PATH=/opt/proveo/chromium/chrome
RUN set -e; \
    case "$(uname -m)" in \
      x86_64|amd64) ab_arch=linux-x64 ;; \
      aarch64|arm64) ab_arch=linux-arm64 ;; \
      *) echo "unsupported architecture for agent-browser: $(uname -m)" >&2; exit 1 ;; \
    esac \
    && curl -fsSL -o /tmp/agent-browser.tgz \
         "https://registry.npmjs.org/agent-browser/-/agent-browser-${AGENT_BROWSER_VERSION}.tgz" \
    && echo "${AGENT_BROWSER_SHA256}  /tmp/agent-browser.tgz" | sha256sum -c - \
    && mkdir -p /opt/agent-browser \
    && tar -xzf /tmp/agent-browser.tgz -C /opt/agent-browser --strip-components=1 \
         "package/bin/agent-browser-${ab_arch}" package/skill-data package/LICENSE package/README.md \
    && install -m 0755 "/opt/agent-browser/bin/agent-browser-${ab_arch}" /usr/local/bin/agent-browser \
    && rm -rf /opt/agent-browser/bin /tmp/agent-browser.tgz \
    && chrome_dir="$(dirname "$(find "$PLAYWRIGHT_BROWSERS_PATH" -maxdepth 3 -type f -name chrome -path '*/chromium-*/*' | head -n1)")" \
    && test -n "$chrome_dir" && test -x "$chrome_dir/chrome" \
    && mkdir -p /opt/proveo && ln -sfn "$chrome_dir" /opt/proveo/chromium \
    && test -x "$AGENT_BROWSER_EXECUTABLE_PATH" \
    && agent-browser --version \
    && test -f "$AGENT_BROWSER_SKILLS_DIR/core/SKILL.md" \
    && chmod -R a+rX /opt/agent-browser \
    && /usr/local/sbin/proveo-harden /usr /opt

COPY skills/ /opt/proveo/skills/

LABEL security.hardened=true \
      org.opencontainers.image.title="proveo harness base (node + LSP + Playwright/Chromium + agent-browser)"
