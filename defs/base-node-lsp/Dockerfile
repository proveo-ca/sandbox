# SPEC: _spec/components.puml, _spec/_devops/image-lineage-and-publish.puml, _spec/packages/lib/steps.puml
ARG BASE_IMAGE=proveo/base-node:latest
FROM ${BASE_IMAGE}

# The prune is in this SAME RUN, because a later one would keep the bytes.
#
# Measured in the installed tree: 11 MB of `*.map` and 18 MB of changelogs, readmes
# and docs/ directories. Source maps only ever improve a stack trace from a server
# nobody debugs from inside a sandbox. `test/` directories are deliberately NOT
# pruned — some packages load fixtures from them at runtime, and 29 MB is not worth
# finding out which.
# SPEC: _spec/_plans/image-size-reduction.puml
RUN npm install -g --no-optional \
    typescript-language-server \
    vscode-langservers-extracted \
    bash-language-server \
    dockerfile-language-server-nodejs \
    yaml-language-server \
    pyright \
    && find /usr/lib/node_modules \( -name '*.map' -o -iname 'CHANGELOG*' \
         -o -iname 'README*' -o -iname '*.markdown' \) -type f -delete \
    && find /usr/lib/node_modules -type d -name docs -prune -exec rm -rf {} + \
    && npm cache clean --force \
    && rm -rf /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden /usr /opt

LABEL security.hardened=true \
      org.opencontainers.image.title="proveo harness base (node + LSP servers)"
