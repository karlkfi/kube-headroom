# Build the manager binary. --platform=$BUILDPLATFORM keeps the compile stage
# on the build host's native arch so multi-arch builds cross-compile via
# GOOS/GOARCH below instead of emulating the Go toolchain under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the module manifests and the vendored dependency tree first, so this
# large, rarely-changing layer stays cached across app-source-only edits. Deps
# are vendored, so there is no `go mod download` step and the build needs no
# network.
COPY go.mod go.mod
COPY go.sum go.sum
COPY vendor/ vendor/

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# Build
# the GOARCH has no default value to allow the binary to be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
# -mod=vendor builds strictly from vendor/ and turns an inconsistent vendor tree
# into a hard error instead of a silent network fetch.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -mod=vendor -a -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
