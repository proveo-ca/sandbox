# SPEC: _spec/defs/claudecode/claudecode-egress-topology.puml, _spec/internal/egressproxy/mitm-and-flow-record.puml
#
# Build context is the REPO ROOT (the Go module), not this directory:
#   docker build -f defs/sidecars/egress-proxy/Dockerfile -t proveo/egress-proxy:latest .

ARG GO_VERSION=1.26
FROM golang:${GO_VERSION} AS build
WORKDIR /src
# Cache module downloads separately from source.
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# Static, stripped binary — runnable as any uid against bind mounts.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/proveo-egress ./cmd/proveo-egress

# Distroless static: ships ca-certificates (needed to validate the real provider
# cert on the re-originated TLS leg) and no shell/package manager.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/proveo-egress /usr/local/bin/proveo-egress
EXPOSE 8888
ENTRYPOINT ["/usr/local/bin/proveo-egress"]
