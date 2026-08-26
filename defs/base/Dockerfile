# SPEC: _spec/components.puml, _spec/_runtimes/toolchain-provisioning.puml

# Single builder for proveo-entrypoint; every harness inherits it from here
# instead of recompiling it in its own stage. Context is the repo root.
FROM golang:1.26-trixie AS proveo-entrypoint-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/proveo-entrypoint ./cmd/proveo-entrypoint
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/ptdewey/plantuml-lsp@latest

FROM debian:trixie-slim

COPY defs/base/proveo-harden /usr/local/sbin/proveo-harden
COPY --from=proveo-entrypoint-builder /out/proveo-entrypoint /out/plantuml-lsp /usr/local/bin/

RUN chmod +x /usr/local/sbin/proveo-harden /usr/local/bin/proveo-entrypoint /usr/local/bin/plantuml-lsp \
    && apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
         -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
         > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends \
       git gh bash dumb-init passwd jq tmux ripgrep default-jre-headless graphviz \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden

ARG PLANTUML_VERSION=1.2026.6
RUN curl -fsSL -o /opt/plantuml.jar \
      "https://github.com/plantuml/plantuml/releases/download/v${PLANTUML_VERSION}/plantuml-${PLANTUML_VERSION}.jar" \
    && printf '#!/bin/sh\nexec java -Djava.awt.headless=true -jar /opt/plantuml.jar "$@"\n' \
       > /usr/local/bin/plantuml \
    && chmod 0755 /usr/local/bin/plantuml \
    && curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh \
    && chmod 0755 /usr/local/bin/mise \
    && rm -rf /tmp/* /var/tmp/* /root/.cache \
    && /usr/local/sbin/proveo-harden /usr /opt

ENV DEBIAN_FRONTEND=noninteractive \
    NPM_CONFIG_AUDIT=false \
    NPM_CONFIG_FUND=false \
    RLIMIT_CORE=0 \
    RLIMIT_NOFILE=1024 \
    COREPACK_ENABLE_DOWNLOAD_PROMPT=0

LABEL security.hardened=true \
      org.opencontainers.image.title="proveo harness base"
