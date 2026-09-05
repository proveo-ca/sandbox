# SPEC: _spec/components.puml, _spec/_runtimes/toolchain-provisioning.puml

FROM golang:1.26-trixie AS proveo-entrypoint-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/ptdewey/plantuml-lsp@latest
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/proveo-entrypoint ./cmd/proveo-entrypoint

# SPEC: _spec/_plans/image-size-reduction.puml
FROM debian:trixie-slim AS jre-builder
RUN apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends openjdk-21-jdk-headless binutils \
    && jlink --add-modules \
         java.base,java.compiler,java.desktop,java.instrument,java.logging,java.management,java.naming,java.net.http,java.prefs,java.rmi,java.scripting,java.sql,java.xml,jdk.attach,jdk.compiler,jdk.crypto.ec,jdk.jartool,jdk.jdi,jdk.unsupported,jdk.zipfs \
         --strip-debug --no-header-files --no-man-pages --compress=zip-6 --output /opt/jre \
    && /opt/jre/bin/java -version

FROM debian:trixie-slim

ARG DEBIAN_FRONTEND=noninteractive

COPY --chmod=0755 defs/base/proveo-harden /usr/local/sbin/proveo-harden
COPY --chmod=0755 --from=proveo-entrypoint-builder /out/proveo-entrypoint /out/plantuml-lsp /usr/local/bin/

RUN apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
         -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
         > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends \
       git gh bash dumb-init passwd jq tmux ripgrep graphviz \
       fontconfig fonts-dejavu-core libharfbuzz0b \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden

COPY --from=jre-builder /opt/jre /opt/jre

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

ENV NPM_CONFIG_AUDIT=false \
    NPM_CONFIG_FUND=false \
    COREPACK_ENABLE_DOWNLOAD_PROMPT=0

LABEL security.hardened=true \
      org.opencontainers.image.title="proveo harness base"
