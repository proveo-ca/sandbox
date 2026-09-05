# SPEC: _spec/defs/claudecode/claudecode-egress-topology.puml

ARG MITMPROXY_VERSION=11.1.3
FROM mitmproxy/mitmproxy:${MITMPROXY_VERSION}

ARG MITMPROXY_VERSION

USER root

COPY addons/ndjson_dump.py /addons/ndjson_dump.py
COPY --chmod=0755 entrypoint.sh /entrypoint.sh

VOLUME ["/flows", "/mitmproxy-confdir"]
EXPOSE 8888

LABEL mitmproxy.version="${MITMPROXY_VERSION}"

ENTRYPOINT ["/entrypoint.sh"]
