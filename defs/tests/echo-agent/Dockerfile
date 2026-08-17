# SPEC: _spec/tests/testing-strategy.puml
FROM alpine:3
RUN apk add --no-cache curl jq
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
WORKDIR /app
ENTRYPOINT ["/entrypoint.sh"]
