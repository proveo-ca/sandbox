# SPEC: _spec/defs/claudecode/claudecode-egress-topology.puml, _spec/internal/egressproxy/mitm-and-flow-record.puml

ARG GO_VERSION=1.26
FROM golang:${GO_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=proveo-egress-proxy-gomod,target=/go/pkg/mod,sharing=locked \
    go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,id=proveo-egress-proxy-gomod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=proveo-egress-proxy-gocache,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/proveo-egress ./cmd/proveo-egress

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/proveo-egress /usr/local/bin/proveo-egress
EXPOSE 8888
ENTRYPOINT ["/usr/local/bin/proveo-egress"]
