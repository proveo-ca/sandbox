# SPEC: _spec/components.puml, _spec/_runtimes/toolchain-provisioning.puml
# SPEC: _spec/_devops/sandbox-template-rebase.puml

# DECLARED BEFORE THE FIRST FROM, and it must stay there. An ARG written after a
# FROM belongs to THAT BUILD STAGE, so a copy sitting between the builder stage
# and the runtime stage is invisible to `FROM ${SANDBOX_TEMPLATE}` — buildx warns
# "UndefinedArgInFrom" and then fails with "base name should not be blank".
# Only the global scope, above every FROM, reaches a FROM line.
ARG SANDBOX_TEMPLATE=docker/sandbox-templates:shell-docker-0.5.0

FROM golang:1.26-trixie AS proveo-entrypoint-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/ptdewey/plantuml-lsp@latest
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/proveo-entrypoint ./cmd/proveo-entrypoint

# ── THE SUPPORTED BASE ──────────────────────────────────────────────────────
# "Custom templates are reusable sandbox images that EXTEND ONE OF THE BUILT-IN
# agent environments" — docs.docker.com/ai/sandboxes/customize/templates/. This
# used to descend from debian:trixie-slim, which inherited none of what sbx
# assumes a sandbox carries and left every def re-deriving it: uid 1000 named
# agent, /home/agent, a docker group on gid 1001, sudo. All four arrive here.
#
# The `-docker` suffix is not cosmetic. Plain tags ship the docker CLIENT only;
# the Engine (dockerd, containerd, runc, docker-proxy) rides the -docker
# variants. A client with no daemon behind it is exactly what `docker: sbx`
# promised and no proveo image delivered.
#
FROM ${SANDBOX_TEMPLATE}

# Re-declared: an ARG consumed by FROM does not survive into the build stage,
# so the LABEL at the foot would otherwise record an empty template.
ARG SANDBOX_TEMPLATE
ARG DEBIAN_FRONTEND=noninteractive

# The template's runtime user is `agent`; the build steps below are root's.
# Downstream layers (base-node, base-node-lsp, the defs) apt-install and
# npm -g, so this image ends as root and each LEAF def declares its own USER.
USER root

COPY --chmod=0755 defs/base/proveo-harden /usr/local/sbin/proveo-harden
COPY --chmod=0755 --from=proveo-entrypoint-builder /out/proveo-entrypoint /out/plantuml-lsp /usr/local/bin/

# Only what the template does NOT already carry. It ships git, gh, jq, rg,
# curl, ca-certificates, tini, sudo, uv, and the go/node/python/java
# toolchains, so the GitHub CLI keyring dance this file used to perform is
# gone with the package it installed.
# SPEC: _spec/_plans/image-size-reduction.puml
RUN apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends \
       dumb-init passwd tmux graphviz iptables \
       fontconfig fonts-dejavu-core libharfbuzz0b \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden

# The jlink'd JRE is gone: the template ships a full JDK 25. /opt/jre stays as
# a SYMLINK so JAVA_HOME and every downstream reference to it keep working
# without knowing the arch-specific path underneath.
RUN set -e; \
    java_bin="$(command -v java || true)"; \
    [ -n "${java_bin}" ] || java_bin="$(ls -d /usr/lib/jvm/*/bin/java 2>/dev/null | head -n1)"; \
    [ -n "${java_bin}" ] || { echo "the template ships no java: plantuml and jdtls both need one" >&2; exit 1; }; \
    java_bin="$(readlink -f "${java_bin}")"; \
    java_home="${java_bin%/bin/java}"; \
    test -x "${java_home}/bin/java" || { echo "resolved java_home ${java_home} has no bin/java" >&2; exit 1; }; \
    ln -sfn "${java_home}" /opt/jre; \
    /opt/jre/bin/java -version

ENV JAVA_HOME=/opt/jre \
    PATH="/opt/jre/bin:${PATH}"

ARG MISE_VERSION=v2026.9.1
ARG MISE_SHA256_X64=c98423c8470d6dc416d9f7036d0646d8ef5ae92ad9186907f8fcc84cbe7db4ea
ARG MISE_SHA256_ARM64=0ef0a778eaa8599f3e90a8a0979c9fc3f79922cafb5fa6d39f366d974da33bba
ARG PLANTUML_VERSION=1.2026.6
RUN curl -fsSL -o /opt/plantuml.jar \
      "https://github.com/plantuml/plantuml/releases/download/v${PLANTUML_VERSION}/plantuml-${PLANTUML_VERSION}.jar" \
    && printf '#!/bin/sh\nexec /opt/jre/bin/java -Djava.awt.headless=true -jar /opt/plantuml.jar "$@"\n' \
       > /usr/local/bin/plantuml \
    && chmod 0755 /usr/local/bin/plantuml \
    && printf '@startuml\nAlice -> Bob: floor\n@enduml\n' > /tmp/floor.puml \
    && plantuml -tpng -o /tmp /tmp/floor.puml && test -s /tmp/floor.png \
    && rm -f /tmp/floor.puml /tmp/floor.png \
    && case "$(uname -m)" in \
         x86_64|amd64)  mise_arch=x64;   mise_sha="${MISE_SHA256_X64}" ;; \
         aarch64|arm64) mise_arch=arm64; mise_sha="${MISE_SHA256_ARM64}" ;; \
         *) echo "unsupported architecture for mise: $(uname -m)" >&2; exit 1 ;; \
       esac \
    && curl -fsSL -o /tmp/mise \
         "https://github.com/jdx/mise/releases/download/${MISE_VERSION}/mise-${MISE_VERSION}-linux-${mise_arch}" \
    && echo "${mise_sha}  /tmp/mise" | sha256sum -c - \
    && install -m 0755 /tmp/mise /usr/local/bin/mise \
    && rm -f /tmp/mise \
    && test "v$(mise --version | awk '{print $1}')" = "${MISE_VERSION}" \
    && rm -rf /tmp/* /var/tmp/* /root/.cache \
    && /usr/local/sbin/proveo-harden /usr /opt

# The daemon the template ships must survive the harden pass and be reachable
# from the runtime uid. Both are inherited, and both are asserted here so a
# base that quietly loses either fails at BUILD time rather than in a sandbox.
# SPEC: _spec/_devops/sandbox-template-rebase.puml
RUN set -e; \
    command -v docker  >/dev/null || { echo "template lost the docker client" >&2; exit 1; }; \
    command -v dockerd >/dev/null || { echo "not a -docker template: no Engine" >&2; exit 1; }; \
    getent group docker >/dev/null || { echo "no docker group" >&2; exit 1; }; \
    sudo_bin="$(readlink -f "$(command -v sudo)")"; \
    test -n "${sudo_bin}" || { echo "no sudo in the template" >&2; exit 1; }; \
    test -u "${sudo_bin}" || { echo "proveo-harden disarmed sudo (${sudo_bin})" >&2; exit 1; }

ENV NPM_CONFIG_AUDIT=false \
    NPM_CONFIG_FUND=false \
    COREPACK_ENABLE_DOWNLOAD_PROMPT=0

LABEL security.hardened=true \
      org.opencontainers.image.title="proveo harness base" \
      proveo.base.template="${SANDBOX_TEMPLATE}"
