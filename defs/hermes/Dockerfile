# SPEC: _spec/defs/hermes/hermes-topology.puml, _spec/defs/hermes/hermes-paradigm.puml, _spec/_devops/agent-version-pin.puml
ARG HERMES_AGENT_VERSION

FROM --platform=$BUILDPLATFORM golang:1.26-trixie AS proveo-entrypoint-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=proveo-hermes-gomod,target=/go/pkg/mod,sharing=locked \
    go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,id=proveo-hermes-gomod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=proveo-hermes-gocache,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -ldflags='-s -w' -o /out/proveo-entrypoint ./cmd/proveo-entrypoint

FROM scratch AS proveo-overlay
COPY defs/subagents/ /opt/proveo/subagents/
COPY AGENTS.md /opt/proveo/AGENTS.md
COPY --chmod=0755 --from=proveo-entrypoint-builder /out/proveo-entrypoint /usr/local/bin/
COPY packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY --chmod=0755 packages/lib/proveo-seed /usr/local/bin/proveo-seed
COPY --chmod=0755 packages/lib/hooks/git-sync-turn.sh /opt/proveo/hooks/git-sync-turn.sh
COPY packages/lib/hooks/proveo-git-sync-turn.js /opt/proveo/hooks/proveo-git-sync-turn.js
COPY --chmod=0755 packages/lib/proveo-await-seed /usr/local/bin/proveo-await-seed
COPY --chmod=0755 packages/lib/proveo-host-cdp /usr/local/bin/proveo-host-cdp
COPY --chmod=0755 defs/hermes/entrypoint.sh /entrypoint.sh
COPY --chmod=0755 defs/hermes/browser-launch /opt/proveo/shims/hermes

FROM nousresearch/hermes-agent:${HERMES_AGENT_VERSION} AS hermes-upstream

FROM hermes-upstream

USER root
ARG DEBIAN_FRONTEND=noninteractive
ARG HERMES_AGENT_VERSION
ENV HERMES_UID=1000

# SPEC: _spec/_devops/sandbox-template-rebase.puml, _spec/defs/hermes/hermes-paradigm.puml
RUN --mount=type=cache,id=proveo-hermes-apt-cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,id=proveo-hermes-apt-lists,target=/var/lib/apt,sharing=locked \
    --mount=type=bind,from=proveo-overlay,target=/proveo-overlay \
    test -n "${HERMES_AGENT_VERSION}" \
    && apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends dumb-init curl jq socat \
    && usermod -u 1000 hermes && groupmod -g 1000 hermes \
    && find / -xdev \( -uid 10000 -o -gid 10000 \) -exec chown -h hermes:hermes {} + \
    && install -d -m 0700 -o hermes -g hermes /tmp/hermes-runtime \
    && groupadd -o -g 1000 agent \
    && useradd -o -u 1000 -g 1000 -M -d /home/agent -s /bin/bash agent \
    && install -d -o 1000 -g 1000 /home/agent \
    && install -d -m 0700 -o 1000 -g 1000 /var/lib/proveo/browser \
    && install -d /app \
    && cp -a /proveo-overlay/. /

ENV PATH="/opt/proveo/shims:${PATH}"

LABEL security.non-root=true \
      proveo.agent="hermes-agent" \
      proveo.agent.version="${HERMES_AGENT_VERSION}"

HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
  CMD /opt/hermes/bin/hermes --version >/dev/null 2>&1 || exit 1

# dumb-init replaces upstream's s6-overlay /init: one foreground hermes process per sandbox.
# SPEC: _spec/defs/hermes/hermes-paradigm.puml
ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
