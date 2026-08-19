# SPEC: _spec/components.puml, _spec/_devops/image-lineage-and-publish.puml, _spec/packages/lib/steps.puml
ARG BASE_IMAGE=proveo/base-node:latest
FROM ${BASE_IMAGE}

RUN npm install -g --no-optional \
    typescript-language-server \
    vscode-langservers-extracted \
    bash-language-server \
    dockerfile-language-server-nodejs \
    yaml-language-server \
    pyright \
    && npm cache clean --force \
    && rm -rf /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden /usr /opt

LABEL security.hardened=true \
      org.opencontainers.image.title="proveo harness base (node + LSP servers)"
