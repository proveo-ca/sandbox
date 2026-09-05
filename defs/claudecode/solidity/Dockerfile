# SPEC: _spec/_devops/image-lineage-and-publish.puml, _spec/_devops/agent-version-pin.puml
# SPEC: _spec/_plans/image-size-reduction.puml
ARG BASE_IMAGE=proveo/claudecode:latest
FROM ${BASE_IMAGE}

ARG USER_NAME=claude

USER root

ENV PYTHONPYCACHEPREFIX=/tmp/proveo-pycache

ARG SEMGREP_VERSION=1.176.1
ARG SOLC_SELECT_VERSION=1.2.0
RUN apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends python3-venv \
    && rm -rf /var/lib/apt/lists/* \
    && python3 -m venv /opt/security \
    && /opt/security/bin/pip install --no-cache-dir --no-compile \
         "semgrep==${SEMGREP_VERSION}" "solc-select==${SOLC_SELECT_VERSION}" \
    && /opt/security/bin/pip show semgrep | grep -qx "Version: ${SEMGREP_VERSION}" \
    && /opt/security/bin/pip show solc-select | grep -qx "Version: ${SOLC_SELECT_VERSION}" \
    && ln -sf /opt/security/bin/semgrep /usr/local/bin/semgrep \
    && ln -sf /opt/security/bin/solc-select /usr/local/bin/solc-select \
    && ln -sf /opt/security/bin/solc /usr/local/bin/solc \
    && rm -rf /root/.cache /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden /usr /opt

ARG SOLHINT_VERSION=6.2.4
RUN npm install -g --no-optional "solhint@${SOLHINT_VERSION}" \
    && npm cache clean --force \
    && solhint --version | grep -qF "${SOLHINT_VERSION}" \
    && rm -rf /tmp/* /var/tmp/* \
    && /usr/local/sbin/proveo-harden /usr /opt

USER ${USER_NAME}
RUN solc-select install 0.8.20 && solc-select use 0.8.20

ARG FOUNDRY_VERSION=v1.8.1
RUN curl -L https://foundry.paradigm.xyz | bash \
    && "/home/${USER_NAME}/.foundry/bin/foundryup" --install "${FOUNDRY_VERSION}" \
    && find "/home/${USER_NAME}/.foundry" \( -name anvil -o -name chisel \) -delete \
    && "/home/${USER_NAME}/.foundry/bin/forge" --version \
    && "/home/${USER_NAME}/.foundry/bin/cast" --version \
    && rm -rf "/home/${USER_NAME}/.foundry/cache" /tmp/* /var/tmp/*

ENV PATH="$PATH:/home/${USER_NAME}/.foundry/bin"
WORKDIR /app
