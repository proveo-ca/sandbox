# SPEC: _spec/defs/hermes/hermes-paradigm.puml
ARG BASE_IMAGE=proveo/hermes:latest
FROM ${BASE_IMAGE}

USER root
ARG DEBIAN_FRONTEND=noninteractive
ARG OLLAMA_MODEL_TAG

RUN test -n "${OLLAMA_MODEL_TAG}" \
      || { echo "OLLAMA_MODEL_TAG build-arg is required — 'mise run build hermes-muse-glimmer' resolves it" >&2; exit 1; }

# The ollama installer extracts a zstd archive.
RUN --mount=type=cache,id=proveo-hermes-baked-apt-cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,id=proveo-hermes-baked-apt-lists,target=/var/lib/apt,sharing=locked \
    apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends zstd

RUN curl -fsSL https://ollama.com/install.sh | sh \
    && ollama --version

# Weights live outside /opt/data (the mutable HERMES_HOME volume, sometimes
# bind-mounted by an operator) so a mount never shadows them, and
# world-readable so the non-root "hermes" user this image drops to at
# runtime can load them without needing to be the user that pulled them.
ENV OLLAMA_MODELS=/opt/ollama-models
RUN mkdir -p "${OLLAMA_MODELS}"

# Bake the weights into the image at build time: no runtime pull, no
# sidecar container, no network dependency for inference. Standard
# "start the daemon just long enough to pull, then stop it" pattern for
# baking a model into a Docker layer.
# SPEC: _spec/defs/hermes/hermes-paradigm.puml
RUN ollama serve & \
    sleep 5 && \
    ollama pull "${OLLAMA_MODEL_TAG}" && \
    pkill -f "ollama serve" && \
    sleep 2 && \
    chmod -R a+rX "${OLLAMA_MODELS}"

ENV OLLAMA_BAKED_MODEL_TAG="${OLLAMA_MODEL_TAG}"
LABEL proveo.baked-model="${OLLAMA_MODEL_TAG}"

# Root stays the build user here too, unchanged from the hermes base image:
# entrypoint.sh still needs it to run stage2-hook.sh before dropping
# privilege through hermes's own exec shim.

# Docker inherits ENTRYPOINT from the base stage at the runtime layer, but
# restated explicitly here too: the own-agent-def contract checks each
# Dockerfile under defs/hermes/ for its own literal exec-form ENTRYPOINT line,
# not the image's resolved metadata.
ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
