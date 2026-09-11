# Build the manager binary
# Override BASE_IMAGE to build from another registry, e.g. docker.io/library/golang:1.26
ARG BASE_IMAGE=golang:1.26
# --platform pins the builder to the machine doing the building, so a multi-arch
# build cross-compiles with Go instead of running the whole toolchain under QEMU.
# Without it, `go build -a` rebuilds the standard library emulated for every
# non-native target, which took over 35 minutes for linux/arm64 on an amd64
# runner. Go cross-compiles for free, so TARGETARCH below does the real work.
FROM --platform=${BUILDPLATFORM} ${BASE_IMAGE} AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# Build
# the GOARCH has no default value to allow the binary to be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
