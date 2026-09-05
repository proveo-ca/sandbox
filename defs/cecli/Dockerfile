# cecli = the aider fork (cecli-dev) in a Python venv on proveo/base. No Node, no
# browsers (deduped from the old MCR playwright/python variant).
ARG BASE_IMAGE=proveo/base:latest

# proveo-entrypoint is inherited from proveo/base (built once in defs/base).
FROM ${BASE_IMAGE}

ARG USER_ID=1000
ARG USER_NAME=cecli

RUN if getent passwd "${USER_ID}" >/dev/null; then \
      existing_user="$(getent passwd "${USER_ID}" | cut -d: -f1)"; \
      existing_group="$(getent group "${USER_ID}" | cut -d: -f1)"; \
      if [ "${existing_group}" != "${USER_NAME}" ]; then \
        groupmod -n "${USER_NAME}" "${existing_group}"; \
      fi; \
      if [ "${existing_user}" != "${USER_NAME}" ]; then \
        usermod -l "${USER_NAME}" -d "/home/${USER_NAME}" -m "${existing_user}"; \
      fi; \
      usermod -s /bin/bash "${USER_NAME}"; \
    else \
      groupadd -g "${USER_ID}" "${USER_NAME}" && \
      useradd -m -u "${USER_ID}" -g "${USER_ID}" -s /bin/bash "${USER_NAME}"; \
    fi

# sbx's own templates run as `agent` with HOME=/home/agent, and anything a kit
# mounts lands under that path — which a symlink cannot serve, because a mount
# target does not resolve through one. So the REAL home is /home/agent and the
# harness name is the alias, the same reversal cursor and opencode needed.
# cecli joins them because `docker: sbx` is now the only backend that hands this
# harness a Docker daemon (_spec/_plans/retire-dind.puml).
RUN usermod -d /home/agent -m "${USER_NAME}" \
 && if ! getent group agent >/dev/null; then groupadd -o -g "${USER_ID}" agent; fi \
 && if ! getent passwd agent >/dev/null; then \
      useradd -o -u "${USER_ID}" -g "${USER_ID}" -M -d /home/agent -s /bin/bash agent; \
    fi \
 && { [ -e "/home/${USER_NAME}" ] || ln -s /home/agent "/home/${USER_NAME}"; } \
 && chown -R "${USER_ID}:${USER_ID}" /home/agent

# cecli-specific env; the common floor lives in proveo/base
# PYTHONPYCACHEPREFIX, not PYTHONDONTWRITEBYTECODE.
#
# pip byte-compiles at install time regardless of that variable — it is a RUNTIME
# switch — so both venvs shipped .py AND .pyc: 153 MB measured. `--no-compile`
# above removes them, and the naive pairing (keep DONTWRITEBYTECODE) would then
# re-parse every module on every launch forever: `cecli --version` measured
# 876 ms -> 1850 ms.
#
# So bytecode is written instead, to a prefix OUTSIDE the read-only venv that any
# run-as uid can create. Measured at uid 4242: first launch 2258 ms, then 711 ms —
# faster than the 876 ms baseline, because the cache holds only what was imported.
# /tmp is the right home for it: per-sandbox, world-writable, and nothing needs it
# to survive the sandbox.
# SPEC: _spec/_plans/image-size-reduction.puml
ENV PYTHONPYCACHEPREFIX=/tmp/proveo-pycache \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    CECLI_HOME=/app/.cecli

# build-essential is NOT here, and the venvs below still build. It used to be, so
# pip could compile sdists — measured: of cecli-dev's and serena-agent's entire
# dependency closures exactly ONE arrives as an sdist (proxy_tools 0.1.0) and it is
# pure Python, so setuptools alone builds its wheel. gcc-14, g++-14, cpp-14,
# binutils, libstdc++-dev, libgcc-dev and linux-libc-dev were ~190 MB shipped to an
# agent image for a compiler nothing invoked. Verified by building both venvs on a
# base with no compiler at all before this landed.
# SPEC: _spec/_plans/image-size-reduction.puml
#
# iptables is the daemon's, not cecli's: sbx's per-sandbox daemon builds its own
# NAT chain at start and cannot without it. It stays even though this image no
# longer ships dockerd — a NAT chain needs iptables whoever builds it.
RUN apt-get update -o APT::Update::Error-Mode=any \
    && apt-get install -y --no-install-recommends \
    python3 \
    python3-pip \
    python3-venv \
    iptables \
    less \
  && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
  && /usr/local/sbin/proveo-harden /usr /opt

# cecli-dev is pinned by version, never bare: BuildKit caches this layer by its
# text, so an unpinned install re-runs only when something ABOVE it changes, and a
# warm cache keeps last month's agent with nothing in the build saying so. build.sh
# resolves the current PyPI release (proveo_agent_version) and passes it in, which
# moves the cache key exactly when upstream moves; the version check makes the pin
# a fact about the image. No default on purpose — a bare `docker build` must fail
# here, not bake whatever is newest.
# SPEC: _spec/_devops/agent-version-pin.puml
ARG CECLI_VERSION
RUN test -n "${CECLI_VERSION}" \
      || { echo "CECLI_VERSION build-arg is required — defs/cecli/build.sh resolves it" >&2; exit 1; } \
  && python3 -m venv /opt/cecli \
  && /opt/cecli/bin/pip install --no-compile --upgrade pip setuptools wheel \
  && /opt/cecli/bin/pip install --no-compile "cecli-dev==${CECLI_VERSION}" boto3 botocore \
  && /opt/cecli/bin/pip show cecli-dev | grep -qx "Version: ${CECLI_VERSION}" \
  && rm -rf /root/.cache /tmp/* /var/tmp/* \
  && /usr/local/sbin/proveo-harden /usr /opt

ENV PATH="/opt/cecli/bin:${PATH}"

# Serena MCP server (LSP-backed semantic code tools). cecli has no native LSP
# client, so its code intelligence arrives as MCP — the same delivery cursor gets
# from mcp-language-server. Its own venv, because serena-agent and cecli-dev
# disagree about dependency versions; only the stdio launcher goes on PATH.
#
# PINNED for the reason this layer was deleted the first time: an unpinned
# `pip install serena-agent` shipped a version skew that left the server dead on
# every boot for weeks. The pin makes the version a fact about the image, and
# packages/lib/entrypoint-lib.sh (configure_cecli_mcp) refuses to declare a
# server that will not answer an initialize handshake.
# SPEC: _spec/_devops/agent-version-pin.puml, _spec/defs/cecli/cecli-paradigm.puml
ARG SERENA_VERSION
RUN test -n "${SERENA_VERSION}" \
      || { echo "SERENA_VERSION build-arg is required — defs/cecli/build.sh resolves it" >&2; exit 1; } \
  && python3 -m venv /opt/serena \
  && /opt/serena/bin/pip install --no-cache-dir --no-compile "serena-agent==${SERENA_VERSION}" \
  && /opt/serena/bin/pip show serena-agent | grep -qx "Version: ${SERENA_VERSION}" \
  && ln -sf /opt/serena/bin/serena /usr/local/bin/serena \
  && rm -rf /root/.cache /tmp/* /var/tmp/* \
  && /usr/local/sbin/proveo-harden /usr /opt

# No docker binaries here. The whole static tarball — docker, dockerd, containerd,
# containerd-shim-runc-v2, ctr, runc, docker-proxy, docker-init, 210 MB measured —
# was removed when sbx became the only backend that hands this harness a daemon:
# every one of those binaries overlaps the sandbox runtime that owns it.
#
# The LABEL below, the `docker` group and `iptables` are NOT overlaps and stay:
# sbx's own switch, the group on a root:docker socket, and the NAT chain's
# dependency. The assumption this rests on (sbx supplies the client AND the
# daemon), the evidence in this tree pointing the other way, and the exact symptom
# if it is wrong: _spec/_plans/image-size-reduction.puml.
# SPEC: _spec/_plans/image-size-reduction.puml

# ── Docker-in-sandbox ───────────────────────────────────────────────────────
#
# sbx starts the sandbox's own daemon only when the IMAGE carries
# `com.docker.sandboxes.start-docker`; without it sbx records `dind: false` at
# container creation and never starts one. The socket is root:docker, so the
# runtime user needs the group.
RUN groupadd -f docker \
 && usermod -aG docker "${USER_NAME}" \
 && usermod -aG docker agent
LABEL com.docker.sandboxes.start-docker="true"

WORKDIR /app

COPY packages/lib/entrypoint-lib.sh /entrypoint-lib.sh
COPY --chmod=0755 defs/cecli/entrypoint.sh /usr/local/bin/cecli-entrypoint
COPY defs/cecli/defaults/ /opt/cecli/defaults/
COPY defs/subagents/ /opt/proveo/subagents/
COPY defs/bridges/ /opt/proveo/bridges/
COPY defs/cecli/proveo-lib/ /opt/proveo/lib/
RUN chown "${USER_NAME}:${USER_NAME}" /app

# Default to the non-root build user; the wrapper overrides the runtime uid with
# `--user $(id -u):$(id -g)` (see ensure_runtime_user for arbitrary-uid handling).
USER ${USER_NAME}

# What agent the image carries, readable without running it:
#   docker image inspect -f '{{index .Config.Labels "proveo.agent.version"}}' proveo/cecli:local
LABEL proveo.agent="cecli-dev" \
      proveo.agent.version="${CECLI_VERSION}"

ENTRYPOINT ["dumb-init", "--", "cecli-entrypoint"]
CMD ["cecli"]
