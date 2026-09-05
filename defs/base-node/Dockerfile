# SPEC: _spec/components.puml, _spec/_devops/image-lineage-and-publish.puml, _spec/_runtimes/toolchain-provisioning.puml
ARG BASE_IMAGE=proveo/base:latest
FROM ${BASE_IMAGE}

ARG NODE_MAJOR=22

# unzip is Bun's one documented prerequisite on Linux; it lands here so every
# Node consumer inherits it (opencode used to install its own copy).
#
# `rm -rf /usr/include/node` in this SAME RUN: nodesource's deb ships 65 MB of C++
# headers for building native addons, and node-gyp fetches its own into
# ~/.cache/node-gyp anyway — only an explicit `--nodedir` reads these. A later RUN
# would delete the path and keep the bytes, so it has to be here.
# SPEC: _spec/_plans/image-size-reduction.puml
RUN curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - \
    && apt-get install -y --no-install-recommends nodejs unzip \
    && npm install -g pnpm@10.33.0 \
    && npm cache clean --force \
    && rm -rf /usr/include/node \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden /usr /opt

# ── Bun — the TypeScript toolkit's second runtime and package manager ──────────
# SPEC: _spec/_runtimes/toolchain-provisioning.puml
#
# The floor ships ONE node, ONE pnpm and now ONE bun; a repository pins its own,
# and the seed honours the pin (ensure_node_toolchain: corepack for pnpm/yarn/npm,
# mise for bun — corepack does not manage bun). The seed already chose
# `bun install --frozen-lockfile` for a bun.lock / bun.lockb tree; without this
# layer that choice ended in "bun is not on PATH" on every Bun project.
#
# From the GitHub release zip, pinned by version AND by the digest published in
# that release's SHASUMS256.txt — not `npm install -g bun` (its postinstall fetches
# the same binary through the registry, unverified by anything this file names)
# and not the install script (unpinned by default, writes to ~/.bun). x64 is the
# AVX2 build; Bun's `-baseline` variant exists for pre-Haswell CPUs and is not
# what this fleet runs on. `bunx` is a symlink to `bun`, as the installer makes it.
ARG BUN_VERSION=1.4.0
ARG BUN_SHA256_X64=2d03fb5fb83ac8b567aca0a281b2ce1a1a19d488f56c2968d88c3f25e92fe452
ARG BUN_SHA256_AARCH64=4b1a332ee861983eb93bcfe6f770fff94e3e31b2c388bdaea3c8ed35e58eed0e
RUN set -e; \
    case "$(uname -m)" in \
      x86_64|amd64)  bun_arch=x64;     bun_sha="${BUN_SHA256_X64}" ;; \
      aarch64|arm64) bun_arch=aarch64; bun_sha="${BUN_SHA256_AARCH64}" ;; \
      *) echo "unsupported architecture for bun: $(uname -m)" >&2; exit 1 ;; \
    esac \
    && curl -fsSL -o /tmp/bun.zip \
         "https://github.com/oven-sh/bun/releases/download/bun-v${BUN_VERSION}/bun-linux-${bun_arch}.zip" \
    && echo "${bun_sha}  /tmp/bun.zip" | sha256sum -c - \
    && unzip -q -j /tmp/bun.zip "bun-linux-${bun_arch}/bun" -d /usr/local/bin \
    && chmod 0755 /usr/local/bin/bun \
    && ln -sfn bun /usr/local/bin/bunx \
    && test "$(bun --version)" = "${BUN_VERSION}" \
    && rm -f /tmp/bun.zip \
    && /usr/local/sbin/proveo-harden /usr /opt

LABEL security.hardened=true \
      org.opencontainers.image.title="proveo harness base (node + pnpm + bun)"
