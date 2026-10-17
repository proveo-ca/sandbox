# SPEC: _spec/defs/hermes/hermes-topology.puml, _spec/defs/hermes/hermes-paradigm.puml
ARG HERMES_AGENT_VERSION=v2026.9.24

FROM golang:1.26-trixie AS proveo-entrypoint-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=proveo-hermes-gomod,target=/go/pkg/mod,sharing=locked \
    go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,id=proveo-hermes-gomod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=proveo-hermes-gocache,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/proveo-entrypoint ./cmd/proveo-entrypoint

FROM nousresearch/hermes-agent:${HERMES_AGENT_VERSION}

USER root
ARG DEBIAN_FRONTEND=noninteractive
# An ARG used before FROM is scoped to the FROM line alone; re-declaring it
# here is what makes it visible to the LABEL below (docker warns
# UndefinedVar and silently empties the label without this).
ARG HERMES_AGENT_VERSION

# Matches the build-time uid below, so stage2-hook.sh's root-start remap is a no-op.
ENV HERMES_UID=1000

RUN --mount=type=cache,id=proveo-hermes-apt-cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,id=proveo-hermes-apt-lists,target=/var/lib/apt,sharing=locked \
    apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends dumb-init

# proveo launches the agent as uid 1000, and stage2-hook.sh refuses any non-root
# uid other than hermes's own — so hermes IS uid/gid 1000 in this image.
# SPEC: _spec/defs/hermes/hermes-paradigm.puml
RUN usermod -u 1000 hermes && groupmod -g 1000 hermes \
    && find / -xdev \( -uid 10000 -o -gid 10000 \) -exec chown -h hermes:hermes {} + \
    && install -d -m 0700 -o hermes -g hermes /tmp/hermes-runtime

# sbx runs every kit step and its clone as the template user `agent`; this base
# is not an sbx template, so uid 1000 answers to that name too.
# SPEC: _spec/defs/hermes/hermes-paradigm.puml
RUN groupadd -o -g 1000 agent \
    && useradd -o -u 1000 -g 1000 -M -d /home/agent -s /bin/bash agent \
    && install -d -o 1000 -g 1000 /home/agent

RUN mkdir -p /opt/proveo/subagents /opt/proveo/hooks /opt/proveo/shims
COPY defs/subagents/ /opt/proveo/subagents/
COPY AGENTS.md /opt/proveo/AGENTS.md
COPY --chmod=0755 --from=proveo-entrypoint-builder /out/proveo-entrypoint /usr/local/bin/
COPY packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY --chmod=0755 packages/lib/proveo-seed /usr/local/bin/proveo-seed
COPY --chmod=0755 packages/lib/hooks/git-sync-turn.sh /opt/proveo/hooks/git-sync-turn.sh
COPY packages/lib/hooks/proveo-git-sync-turn.js /opt/proveo/hooks/proveo-git-sync-turn.js
COPY --chmod=0755 packages/lib/proveo-await-seed /usr/local/bin/proveo-await-seed
COPY --chmod=0755 defs/hermes/entrypoint.sh /entrypoint.sh

# Wraps upstream's exec shim, which already drops root to the hermes user (s6-setuidgid).
RUN printf '#!/bin/sh\nexec /usr/local/bin/proveo-await-seed /opt/hermes/bin/hermes "$@"\n' > /opt/proveo/shims/hermes \
    && chmod 0755 /opt/proveo/shims/hermes

WORKDIR /app
ENV PATH="/opt/proveo/shims:${PATH}"

LABEL security.non-root=true \
      proveo.agent="hermes-agent" \
      proveo.agent.version="${HERMES_AGENT_VERSION}"

HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
  CMD /opt/hermes/bin/hermes --version >/dev/null 2>&1 || exit 1

# dumb-init replaces upstream's s6-overlay /init: one foreground hermes process per sandbox.
# SPEC: _spec/defs/hermes/hermes-paradigm.puml
ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
