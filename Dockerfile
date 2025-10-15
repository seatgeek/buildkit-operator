# Build the operator binary
FROM golang:1.25.3-alpine3.21 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Install dependencies
COPY go.mod go.mod
COPY go.sum go.sum
COPY api/ api/
RUN --mount=type=cache,target=/root/go/pkg/ \
    go mod download -x

# Copy the source code
COPY cmd/ cmd/
COPY internal/ internal/

# Build
RUN --mount=type=cache,target=/root/go/pkg \
    CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH} \
    go build -a -o operator cmd/operator/main.go

# Default final stage - builds from scratch
# Use distroless as minimal base image to package the operator binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot as final
WORKDIR /
COPY --from=builder /workspace/operator .
USER 65532:65532

ENTRYPOINT ["/operator"]

# Alternative approach for binaries built outside of the Dockerfile
FROM gcr.io/distroless/static:nonroot AS final_prebuilt
ARG TARGETARCH
WORKDIR /
COPY operator-${TARGETARCH} operator
USER 65532:65532
ENTRYPOINT ["/operator"]

# Default target
FROM final
