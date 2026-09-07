# SPEC: _spec/components.puml, _spec/_devops/image-lineage-and-publish.puml, _spec/packages/lib/steps.puml
ARG BASE_IMAGE=proveo/base-node:latest
FROM ${BASE_IMAGE}

# The slimming pass asks npm where globals actually live: the sandbox template
# sets NPM_CONFIG_PREFIX=/usr/local/share/npm-global, so the hardcoded
# /usr/lib/node_modules this used to walk would match nothing and delete nothing.
# SPEC: _spec/_plans/image-size-reduction.puml, _spec/_devops/sandbox-template-rebase.puml
RUN npm install -g --no-optional \
    typescript-language-server \
    vscode-langservers-extracted \
    bash-language-server \
    dockerfile-language-server-nodejs \
    yaml-language-server \
    pyright \
    && npm_root="$(npm root -g)" \
    && find "$npm_root" \( -name '*.map' -o -iname 'CHANGELOG*' \
         -o -iname 'README*' -o -iname '*.markdown' \) -type f -delete \
    && find "$npm_root" -type d -name docs -prune -exec rm -rf {} + \
    && npm cache clean --force \
    && rm -rf /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden /usr /opt

LABEL security.hardened=true \
      org.opencontainers.image.title="proveo harness base (node + LSP servers)"
