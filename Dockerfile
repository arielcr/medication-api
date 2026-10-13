# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Build stage: compile a static Go binary.
#
# --platform=$BUILDPLATFORM runs the compiler natively on the build machine
# and cross-compiles for the target platform (TARGETOS/TARGETARCH), which
# is much faster than emulating ARM on x86 (or vice versa) for multi-arch
# images: docker buildx build --platform linux/amd64,linux/arm64 .
# ---------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

WORKDIR /src

# Copy only the module files first: this layer (the downloaded
# dependencies) stays cached until go.mod or go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/medication-api ./cmd/medication-api

# ---------------------------------------------------------------------------
# Runtime stage: distroless "static" has no shell and no package manager,
# only CA certificates, time zone data and /etc/passwd. Less software means
# fewer vulnerabilities to patch. The :nonroot tag runs as UID 65532.
# ---------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=build /out/medication-api /medication-api

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/medication-api"]
