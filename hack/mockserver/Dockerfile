# Test double for Prometheus + Loki used by the CI kind smoke test (and the
# local "run the whole application" demo). It serves canned signal data that
# deterministically triggers the rule-based agent's connection-pool-exhausted
# scale-up, so the smoke test can assert a real, log-pattern-driven decision
# without a full monitoring stack. Never for production.
#
# Build from the repo root:  docker build -f hack/mockserver/Dockerfile -t mockserver:ci .
FROM golang:1.21 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY hack/mockserver/ hack/mockserver/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -a -o mockserver ./hack/mockserver

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/mockserver .
USER 65532:65532
EXPOSE 9099
ENTRYPOINT ["/mockserver"]
