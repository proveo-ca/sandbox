# SPEC: _spec/_devops/image-lineage-and-publish.puml
ARG BASE_IMAGE=proveo/claudecode:latest
FROM ${BASE_IMAGE}

ARG USER_NAME=claude

USER root

# Python security tools (system-wide)
RUN pip3 install --no-cache-dir --break-system-packages \
    semgrep \
    solc-select

# Solidity linter
RUN npm install -g --no-optional solhint && npm cache clean --force

USER ${USER_NAME}
RUN solc-select install 0.8.20 && solc-select use 0.8.20
RUN curl -L https://foundry.paradigm.xyz | bash \
    && /home/${USER_NAME}/.foundry/bin/foundryup

ENV PATH="/home/${USER_NAME}/.foundry/bin:$PATH"
WORKDIR /app
