# SPEC: _spec/defs/hermes/hermes-topology.puml, _spec/defs/hermes/hermes-paradigm.puml
ARG HERMES_AGENT_VERSION=v2026.9.24
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

RUN mkdir -p /opt/proveo/subagents /opt/proveo/lib /opt/proveo/hooks /opt/proveo/shims
COPY defs/subagents/ /opt/proveo/subagents/
COPY defs/lib/ /opt/proveo/lib/
COPY AGENTS.md /opt/proveo/AGENTS.md
COPY packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY --chmod=0755 packages/lib/proveo-seed /usr/local/bin/proveo-seed
COPY --chmod=0755 packages/lib/hooks/git-sync-turn.sh /opt/proveo/hooks/git-sync-turn.sh
COPY packages/lib/hooks/proveo-git-sync-turn.js /opt/proveo/hooks/proveo-git-sync-turn.js
COPY --chmod=0755 packages/lib/proveo-await-seed /usr/local/bin/proveo-await-seed
COPY --chmod=0755 defs/hermes/entrypoint.sh /entrypoint.sh

# hermes's own exec shim (docker/hermes-exec-shim.sh, baked upstream at
# /opt/hermes/bin/hermes) already drops root -> the "hermes" user via
# s6-setuidgid when invoked as root, so the proveo shim wraps THAT path rather
# than re-deriving a privilege drop of our own.
RUN printf '#!/bin/sh\nPROVEO_INSTRUCTIONS_MARKER=/dev/shm/proveo-hooks-seeded exec /usr/local/bin/proveo-await-seed /opt/hermes/bin/hermes "$@"\n' > /opt/proveo/shims/hermes \
    && chmod 0755 /opt/proveo/shims/hermes

WORKDIR /app
ENV PATH="/opt/proveo/shims:${PATH}"

LABEL security.non-root=true \
      proveo.agent="hermes-agent" \
      proveo.agent.version="${HERMES_AGENT_VERSION}"

HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
  CMD /opt/hermes/bin/hermes --version >/dev/null 2>&1 || exit 1

# dumb-init replaces upstream's s6-overlay ENTRYPOINT deliberately: this def
# runs hermes as ONE foreground process per sandbox, matching every other
# harness here, not upstream's persistent multi-service stack (web dashboard +
# per-profile gateways) built for a standing server. Root stays the build
# user (unlike every other def) because entrypoint.sh must run upstream's own
# stage2-hook.sh — UID/GID remap, /opt/data volume ownership, config seeding —
# before dropping privilege through hermes's own exec shim.
# SPEC: _spec/defs/hermes/hermes-paradigm.puml
ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
