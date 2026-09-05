# SPEC: _spec/tests/testing-strategy.puml
FROM alpine:3
RUN apk add --no-cache curl jq
COPY --chmod=0755 entrypoint.sh /entrypoint.sh
WORKDIR /app
ENTRYPOINT ["/entrypoint.sh"]
