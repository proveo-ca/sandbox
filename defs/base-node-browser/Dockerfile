# SPEC: _spec/components.puml, _spec/_devops/image-lineage-and-publish.puml, _spec/defs/browser-layer.puml
ARG BASE_IMAGE=proveo/base-node-lsp:latest
FROM ${BASE_IMAGE}

ARG PLAYWRIGHT_VERSION=1.61.0

ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright

# `--no-shell`: `playwright install chromium` lands TWO browsers — `chromium-<rev>`
# and `chromium_headless_shell-<rev>` — and this layer only ever launches the first.
# The symlink below proves it: its `find … -path '*/chromium-*/*'` uses a hyphen so
# it cannot match the shell. Measured at 1.61.0: chromium 624 MB, shell **334 MB**,
# i.e. a quarter of this layer installed and then excluded by hand. Playwright ≥1.49
# prefers the shell for its own headless runs, so a project's `playwright test`
# falls back to full Chromium — slower to start, same result — while agent-browser,
# which drives Chromium over CDP, never wanted the shell at all.
#
# The `! ls` guard is the point of the flag: a future Playwright that ignores
# `--no-shell` must fail this build loudly rather than quietly re-adding 334 MB.
RUN npm install -g --no-optional "playwright@${PLAYWRIGHT_VERSION}" \
    && playwright install --with-deps --no-shell chromium \
    && ! ls -d "$PLAYWRIGHT_BROWSERS_PATH"/chromium_headless_shell-* >/dev/null 2>&1 \
    && chmod -R a+rX "$PLAYWRIGHT_BROWSERS_PATH" \
    && npm cache clean --force \
    && rm -rf /tmp/* /var/tmp/* /var/lib/apt/lists/* \
    && /usr/local/sbin/proveo-harden /usr /opt

# ── agent-browser (vercel-labs), beside Playwright ─────────────────────────────
# SPEC: _spec/defs/browser-layer.puml
#
# agent-browser is a native Rust CLI that drives Chromium over CDP and answers
# with accessibility-tree snapshots — the harness-neutral browser tool every
# `-browser` variant gets, where Playwright stays the test-runner and library.
#
# Installed from the npm TARBALL, not `npm install -g`, for three reasons:
#   1. the package declares engines.node >= 24 while this floor is Node 22 LTS —
#      the JS launcher only execs the native binary, so the runtime is irrelevant,
#      but npm would still warn on every build;
#   2. the tarball ships every platform's binary (~40 MB); only this arch's is
#      kept, and the skills that `agent-browser skills get core` serves are a
#      separate tree the binary alone does not carry ("Skills directory not
#      found" without AGENT_BROWSER_SKILLS_DIR);
#   3. `agent-browser install` would download a SECOND Chromium (Chrome for
#      Testing). Playwright's is already here, so AGENT_BROWSER_EXECUTABLE_PATH
#      points at it through a revision-stable symlink — Playwright names its store
#      by build revision (chromium-1228/…), and an env var cannot glob.
#
# Verified 2026-09-01 on the darwin build of 0.36.0: with the executable path set
# it launches headless and never asks for `install`. The Linux run-as-uid path
# (containers auto-enable --no-sandbox since 0.16.1) is asserted by
# ensure.sh's floor probe and defs/*/tests, not here: a buildkit RUN is not the
# container it will run in.
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

# The discovery stub each harness seeds into its user skills dir (proveo_seed →
# proveo_seed_browser_skills). proveo's own wording: the vendor stub tells the
# agent to `npm i -g` and `agent-browser install`, neither of which applies here.
COPY skills/ /opt/proveo/skills/

LABEL security.hardened=true \
      org.opencontainers.image.title="proveo harness base (node + LSP + Playwright/Chromium + agent-browser)"
