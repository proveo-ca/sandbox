# SPEC: _spec/components.puml, _spec/_devops/image-lineage-and-publish.puml, _spec/_runtimes/toolchain-provisioning.puml
ARG BASE_IMAGE=proveo/base:latest
FROM ${BASE_IMAGE}

ARG DEBIAN_FRONTEND=noninteractive

# Node arrives as the OFFICIAL nodejs.org tarball, pinned and sha256-verified,
# the same way this lineage already installs mise, bun and plantuml.
#
# It used to come from NodeSource, which publishes PER UBUNTU RELEASE. The rebase
# onto docker/sandbox-templates moved the floor to ubuntu:questing (26.04), and a
# base whose Node install depends on a third party having packaged the exact
# distro release the TEMPLATE happens to ship is a build that breaks on someone
# else's schedule. The tarball has no distro coupling at all.
#
# The template already carries Ubuntu's nodejs (24.x with npm 9.2.0). This
# REPLACES it outright. Putting /usr/local/bin ahead on PATH is not enough: the
# distro copy stays on disk and `type -a node` reports two, which
# e2e TestToolchainShipsNoDuplicateBinaries reads as one tool installed in two
# layers — and it is right to. NodeSource used to replace the package in place,
# so the duplicate is new with the tarball. Purging the distro nodejs after the
# tarball lands removes 361 packages (Ubuntu's node-* module set) and 293 MB
# from /usr, measured — the version and the resolved path are then asserted so a
# silent fallback to a distro copy fails the build.
#
# xz-utils is installed BEFORE the extract, not after: `tar -J` shells out to xz,
# and the template is not guaranteed to carry it. Ordering it after the extract
# would fail on a missing decompressor with tar's own error, not ours.
# SPEC: _spec/_runtimes/toolchain-provisioning.puml
# SPEC: _spec/_devops/sandbox-template-rebase.puml
ARG NODE_VERSION=22.23.2
ARG NODE_SHA256_X64=d60acfe00a2932254bb0ad20e01b0d74397a0875595de719654b214f4b03f307
ARG NODE_SHA256_ARM64=fff4078c5def658577f92c88db7db3bc0072924bfb93fe52c1e744a54e94abb8
RUN set -e; \
    case "$(uname -m)" in \
      x86_64|amd64)  node_arch=x64;   node_sha="${NODE_SHA256_X64}" ;; \
      aarch64|arm64) node_arch=arm64; node_sha="${NODE_SHA256_ARM64}" ;; \
      *) echo "unsupported architecture for node: $(uname -m)" >&2; exit 1 ;; \
    esac; \
    apt-get update -o APT::Update::Error-Mode=any; \
    apt-get install -y --no-install-recommends unzip xz-utils; \
    curl -fsSL -o /tmp/node.tar.xz \
      "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${node_arch}.tar.xz"; \
    echo "${node_sha}  /tmp/node.tar.xz" | sha256sum -c -; \
    tar -xJf /tmp/node.tar.xz -C /usr/local --strip-components=1 \
        --exclude='*/CHANGELOG.md' --exclude='*/LICENSE' --exclude='*/README.md'; \
    rm -f /tmp/node.tar.xz; \
    test "$(node --version)" = "v${NODE_VERSION}"; \
    npt="$(npm --version)"; echo "npm ${npt} from the pinned tarball"; \
    apt-get purge -y --auto-remove nodejs npm; \
    hash -r; \
    test "$(command -v node)" = "/usr/local/bin/node"; \
    test "$(node --version)" = "v${NODE_VERSION}"; \
    npm install -g pnpm@10.33.0; \
    npm cache clean --force; \
    rm -rf /usr/include/node /usr/local/include/node; \
    rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*; \
    /usr/local/sbin/proveo-harden /usr /opt

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
