# Test-only fixture image for the Layer-4 promptful E2E (_spec/tests/testing-strategy.puml).
# A minimal agent: reads a prompt, calls the local model via the OpenAI-compatible
# endpoint the --local-model bridge provides, writes the reply as a side effect.
# NOT a shipped harness — it exists so agent-E2E runs without a vendor image.
FROM alpine:3
RUN apk add --no-cache curl jq
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
WORKDIR /app
ENTRYPOINT ["/entrypoint.sh"]
