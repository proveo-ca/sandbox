# SPEC: _spec/components.puml, _spec/_runtimes/toolchain-provisioning.puml

# Single builder for proveo-entrypoint; every harness inherits it from here
# instead of recompiling it in its own stage. Context is the repo root.
FROM golang:1.26-trixie AS proveo-entrypoint-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# plantuml-lsp is fetched from upstream and owes nothing to this repo's source, so
# it goes ABOVE the copy: below it, every edit in the tree re-ran the install.
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/ptdewey/plantuml-lsp@latest
# `COPY cmd` + `COPY internal`, never `COPY . .` — the same shape
# defs/sidecars/egress-proxy/Dockerfile already uses.
#
# The context is the REPO ROOT (every def builds from there), which carries 201 MB
# of node_modules and 83 MB of apps/. `COPY . .` baked all of it into this layer and
# invalidated the build below on any file in the tree — a README edit re-ran the
# compile. `./cmd/proveo-entrypoint` needs internal/entrypoint and internal/verify
# and nothing else: the root package's embeds are not in its dependency graph, so
# defs/ and packages/ are not required here. The root .dockerignore stops the rest
# from reaching the daemon at all.
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/proveo-entrypoint ./cmd/proveo-entrypoint

# ── The JRE PlantUML runs on, and nothing more ────────────────────────────────
# `default-jre-headless` was the largest single package in this image — dpkg
# Installed-Size 198,806 kB — and its only consumer is /opt/plantuml.jar. Because
# it sits in proveo/base it was paid by every harness, browser variant and sidecar
# descendant, all 14 published images.
#
# jlink builds a runtime holding only the modules that are actually reached.
# MEASURED on this JDK: 72 MB against 194 MB, and it renders -tpng and -tsvg
# identically (verified before this landed).
#
# The module list is NOT the minimum. A plantuml-only prune is 59 MB, and it would
# break jdtls: _install_jdtls provisions its own Java 21 only when `_java_major`
# reports < 21, and this runtime reports 21 — so it must be able to HOST jdtls, an
# Eclipse/OSGi application. java.compiler, java.instrument, jdk.attach, jdk.jdi,
# jdk.zipfs and jdk.compiler are there for it, and they cost 13 MB.
# SPEC: _spec/_plans/image-size-reduction.puml
FROM debian:trixie-slim AS jre-builder
RUN apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends openjdk-21-jdk-headless binutils \
    && jlink --add-modules \
         java.base,java.compiler,java.desktop,java.instrument,java.logging,java.management,java.naming,java.net.http,java.prefs,java.rmi,java.scripting,java.sql,java.xml,jdk.attach,jdk.compiler,jdk.crypto.ec,jdk.jartool,jdk.jdi,jdk.unsupported,jdk.zipfs \
         --strip-debug --no-header-files --no-man-pages --compress=zip-6 --output /opt/jre \
    && /opt/jre/bin/java -version

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
       git gh bash dumb-init passwd jq tmux ripgrep graphviz \
       fontconfig fonts-dejavu-core libharfbuzz0b \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden

# fontconfig, fonts-dejavu-core and libharfbuzz0b above are what
# openjdk-21-jre-headless used to pull in: Debian's libfontmanager.so links against
# the SYSTEM harfbuzz, and without it PlantUML dies in SunFontManager's static
# initialiser before it draws anything. Found by building it, not by reading.
COPY --from=jre-builder /opt/jre /opt/jre

# JAVA_HOME/PATH are set here rather than in the ENV block below because the RUN
# that follows asserts the runtime works, and an ENV at the end of the file is not
# in scope for it. `java` on PATH is also what _install_jdtls probes.
ENV JAVA_HOME=/opt/jre \
    PATH="/opt/jre/bin:${PATH}"

ARG PLANTUML_VERSION=1.2026.6
RUN curl -fsSL -o /opt/plantuml.jar \
      "https://github.com/plantuml/plantuml/releases/download/v${PLANTUML_VERSION}/plantuml-${PLANTUML_VERSION}.jar" \
    && printf '#!/bin/sh\nexec /opt/jre/bin/java -Djava.awt.headless=true -jar /opt/plantuml.jar "$@"\n' \
       > /usr/local/bin/plantuml \
    && chmod 0755 /usr/local/bin/plantuml \
    && printf '@startuml\nAlice -> Bob: floor\n@enduml\n' > /tmp/floor.puml \
    && plantuml -tpng -o /tmp /tmp/floor.puml && test -s /tmp/floor.png \
    && rm -f /tmp/floor.puml /tmp/floor.png \
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
