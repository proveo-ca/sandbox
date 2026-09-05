# SPEC: _spec/components.puml, _spec/_devops/image-lineage-and-publish.puml, _spec/_runtimes/toolchain-provisioning.puml
ARG BASE_IMAGE=proveo/base:latest
FROM ${BASE_IMAGE}

ARG NODE_MAJOR=22

# SPEC: _spec/_plans/image-size-reduction.puml
RUN curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - \
    && apt-get install -y --no-install-recommends nodejs unzip \
    && npm install -g pnpm@10.33.0 \
    && npm cache clean --force \
    && rm -rf /usr/include/node \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden /usr /opt

# SPEC: _spec/_runtimes/toolchain-provisioning.puml
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
