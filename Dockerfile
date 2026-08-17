# Servicesim container image.
#
#   docker build -t servicesim:dev .
#   docker buildx build --platform linux/amd64,linux/arm64 -t servicesim:dev .
#
# Design notes:
#
#   * The runtime stage is `scratch`. Servicesim's entire observable surface is
#     HTTP — the provider listeners and the admin journal — so a shell inside
#     the container buys debugging convenience at the cost of a larger attack
#     surface in every consumer's CI. Introspection goes through /__admin.
#
#   * No CA certificate bundle is installed. Servicesim never dials outward;
#     an unmatched request fails closed rather than being proxied to a real
#     provider (plan security requirement 4). Adding CA certs would only make
#     sense if deliberate live proxying were ever introduced.
#
#   * HEALTHCHECK invokes the binary's own --healthcheck mode instead of wget
#     or curl, which is what lets the runtime stage stay shell-less.

# Pinned to an exact patch so the published image does not silently inherit a
# standard library with known vulnerabilities. govulncheck in CI flagged five in
# go1.26.5 that are fixed here; keep this in step with GO_VERSION in
# .github/workflows/ci.yml.
ARG GO_VERSION=1.26.6

# ============================================================================
# Builder
# ============================================================================
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder

WORKDIR /build

# Module layer caches independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT_SHA=unknown
ARG BUILD_DATE=unknown
ARG TARGETOS
ARG TARGETARCH

# CGO_ENABLED=0 with the default net resolver produces a fully static binary,
# which is what makes a scratch runtime stage possible.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath \
    -ldflags="-w -s \
      -X github.com/c360studio/servicesim.Version=${VERSION} \
      -X github.com/c360studio/servicesim.GitCommit=${COMMIT_SHA} \
      -X github.com/c360studio/servicesim.BuildTime=${BUILD_DATE}" \
    -o /build/servicesim \
    ./cmd/servicesim

# A scratch image has no /etc/passwd. The numeric USER below is sufficient for
# the kernel, but tooling that resolves the UID to a name reads these files, so
# provide a minimal pair rather than leaving lookups to fail.
RUN printf 'servicesim:x:65532:65532:servicesim:/nonexistent:/sbin/nologin\n' > /build/passwd && \
    printf 'servicesim:x:65532:\n' > /build/group

# ============================================================================
# Runtime
# ============================================================================
FROM scratch

COPY --from=builder /build/passwd /etc/passwd
COPY --from=builder /build/group /etc/group
COPY --from=builder /build/servicesim /servicesim

# Built-in protocol scenarios ship inside the image so the container is useful
# with no mount. Product-specific corpora are mounted read-only at /scenarios
# and do not require a Servicesim release.
COPY --from=builder /build/scenarios /scenarios

USER 65532:65532

# Admin (health, readiness, journal), then one listener per provider.
# Values are static — regenerate by comparing against
# `bin/servicesim --print-ports` (admin is always 8080; ours has not moved).
EXPOSE 8080 8081 8082 8083 8084

# The container binds all interfaces; local development defaults to loopback.
ENV SERVICESIM_BIND_ADDRESS=0.0.0.0

HEALTHCHECK --interval=10s --timeout=3s --start-period=2s --retries=3 \
    CMD ["/servicesim", "--healthcheck"]

ENTRYPOINT ["/servicesim"]
CMD ["--scenario", "/scenarios/protocol/happy.yaml"]

ARG VERSION
ARG COMMIT_SHA
ARG BUILD_DATE
LABEL org.opencontainers.image.title="Servicesim" \
      org.opencontainers.image.description="Deterministic HTTP simulator for the Exa, Tavily and Perplexity research APIs, and an MCP server" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT_SHA}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.source="https://github.com/c360studio/servicesim" \
      org.opencontainers.image.vendor="C360" \
      org.opencontainers.image.licenses="MIT"
