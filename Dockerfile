# Build the operator binary
FROM golang:1.24.3-alpine3.21 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Install dependencies
COPY go.mod go.mod
COPY go.sum go.sum
RUN --mount=type=cache,target=/root/go/pkg/ \
    go mod download -x

# Copy the source code
COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/

# Build
RUN --mount=type=cache,target=/root/go/pkg \
    CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH} \
    go build -a -o manager cmd/operator/main.go

# Use distroless as minimal base image to package the operator binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
