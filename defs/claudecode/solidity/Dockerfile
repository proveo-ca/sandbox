# claudecode-solidity: the Solidity/security auditing variant — the mcp harness plus
# Foundry, solc(-select), solhint, and semgrep. Split out of the base image
# because this toolchain alone was ~1.5 GB baked into every claudecode pull.
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

# Per-user toolchains, installed ONCE as the runtime user: solc-select keeps
# its compilers in ~/.solc-select, and the old Foundry root-install-then-cp-
# to-home pattern shipped the 400+ MB toolkit twice.
USER ${USER_NAME}
RUN solc-select install 0.8.20 && solc-select use 0.8.20
RUN curl -L https://foundry.paradigm.xyz | bash \
    && /home/${USER_NAME}/.foundry/bin/foundryup

ENV PATH="/home/${USER_NAME}/.foundry/bin:$PATH"
WORKDIR /workspace
