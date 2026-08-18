# acmesim container image — a consumer's own two-stage build over their own
# composed binary (main.go), in the shape of servicesim's own root
# Dockerfile. Not built by this repository's CI: the image job builds only
# servicesim's own image (cmd/servicesim). `go build ./...` inside this
# module IS a gate — see ../../Taskfile.yml's test:profile task — so this
# file only has to describe a real build, not pass one in CI.
#
# Build it from the REPOSITORY ROOT, not from this directory: go.mod's
# `replace` points at ../.., so the servicesim checkout has to be inside the
# build context or `go mod download` fails with "reading /go.mod: no such file
# or directory". An adopter whose go.mod pins a released servicesim from the
# module proxy instead can use this directory as the context.
#
#   docker build -t acmesim:dev -f examples/profile/Dockerfile .
#
# EXPOSE below is static, the way servicesim's own root Dockerfile's is —
# regenerate by comparing against the built binary's own --print-ports,
# which is what a real consumer's release process would script instead of
# hand-maintaining this comment (docs/proposals/framework-seam.md,
# "image/compose #47-#53").

ARG GO_VERSION=1.26.6

# ============================================================================
# Builder
# ============================================================================
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder

WORKDIR /build

# This module's go.mod points at ../.. through a replace directive, so the
# servicesim checkout it resolves against has to be inside the same build
# context as this file — unlike servicesim's own root Dockerfile, which
# downloads its one dependency from the module proxy, an adopter building
# THIS module's image copies the whole workspace root, not just
# examples/profile.
COPY . /workspace
WORKDIR /workspace/examples/profile
RUN go mod download

ARG VERSION=dev
ARG COMMIT_SHA=unknown
ARG TARGETOS
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath \
    -ldflags="-w -s -X main.version=${VERSION}" \
    -o /build/acmesim \
    .

# A scratch image has no /etc/passwd. The numeric USER below is sufficient
# for the kernel, but tooling that resolves the UID to a name reads these
# files, so provide a minimal pair rather than leaving lookups to fail —
# the same convention servicesim's own root Dockerfile follows.
RUN printf 'acmesim:x:65532:65532:acmesim:/nonexistent:/sbin/nologin\n' > /build/passwd && \
    printf 'acmesim:x:65532:\n' > /build/group

# ============================================================================
# Runtime
# ============================================================================
FROM scratch

COPY --from=builder /build/passwd /etc/passwd
COPY --from=builder /build/group /etc/group
COPY --from=builder /build/acmesim /acmesim

USER 65532:65532

# Admin (health, readiness, journal), then one listener per registered
# profile: acme (8090, this module's own) and exa (8081, servicesim's own
# default — see profiles/exa/profile.go). Confirm with:
#   acmesim --print-ports
EXPOSE 8080 8090 8081

ENV SERVICESIM_BIND_ADDRESS=0.0.0.0

ENTRYPOINT ["/acmesim"]
