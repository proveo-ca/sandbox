# SPEC: _spec/components.puml, _spec/_devops/image-lineage-and-publish.puml
ARG BASE_IMAGE=proveo/base:latest
FROM ${BASE_IMAGE}

ARG NODE_MAJOR=22

RUN curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && npm install -g pnpm@10.33.0 \
    && npm cache clean --force \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden /usr /opt

LABEL security.hardened=true \
      org.opencontainers.image.title="proveo harness base (node)"
