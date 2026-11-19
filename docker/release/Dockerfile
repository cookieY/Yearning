# v4 release image (FE-F13 deliverable; docker/release/Dockerfile).
# Supersedes backend/cmd/yearning/Dockerfile (B13 backend-only image, retained
# unchanged for B13 evidence reproduction): the release image must include
# Gitlink-locked Frontend static assets (F001).
# Multi-stage build: builder uses the same build flags as Makefile backend-release
# (CGO_ENABLED=0, -tags nomsgpack to strip net/rpc linkage, buildinfo version injection);
# final stage uses distroless static nonroot to satisfy the K8s contract (run_as_nonroot,
# read-only rootfs) and carries the locked frontend/dist assets in a dedicated directory —
# sourced exclusively from the Gitlink-checked-out build produced by frontend-release-assets
# in the build context (pinned by the per-Dockerfile ignore allowlist), never dynamically
# pulled from a Frontend default branch or floating tag.
# FRONTEND_DIST_SHA is pinned into the image config via OCI label; after build,
# release_frontend_scan --dist-dir asserts exact tree-hash equality against extracted dist.
# Since BE-B13-LEFTOVER-CLOSURE wired router NoRoute, builder embeds the locked frontend
# assets with the same flags as Makefile backend-release (-tags assets + dist hash injection);
# the image-layer dist copy and OCI label binding remain unchanged.
# Base images are pinned by digest (supply-chain hardening); update digests when upgrading Go.
# Formal Release image Digest/SBOM/multi-arch Index are produced by the Release pipeline (release-manifest contract).
FROM --platform=$BUILDPLATFORM golang:1.26.11@sha256:7af425da11f4fc1a9308eb704369e3f6d1c44e35bac0f874a59ade3f20fdc07e AS builder
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
# Binary links only cmd/ and internal/ (verified via go list -deps); testdata/deploy and
# frontend source/dependencies do not enter the build context — the image consumes only verified static artifacts.
COPY backend/cmd ./cmd
COPY backend/internal ./internal
ARG TARGETOS=linux
ARG TARGETARCH
ARG BUILD_VERSION=dev
ARG BUILD_COMMIT=unknown
ARG FRONTEND_DIST_SHA=missing
ARG VCS_REVISION=unknown
# Same build flags as Makefile backend-release (-tags nomsgpack,assets embeds locked
# frontend assets + dist hash injection; assets/dist is synced into backend/internal by
# frontend-release-assets). Cross-compiled natively on $BUILDPLATFORM via GOOS/GOARCH so
# multi-arch linux/amd64,linux/arm64 builds succeed without QEMU emulation.
# The build context excludes .git (via dockerignore allowlist),
# so -buildvcs cannot stamp VCS metadata here — commit binding is enforced via the
# VCS_REVISION label (asserted equal to HEAD by make after build) and BUILD_COMMIT ldflags,
# combined with the clean-tree + Gitlink lock + dist hash binding asserted by frontend-release-assets.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -tags nomsgpack,assets -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/cookieY/YearningX/backend/internal/platform/buildinfo.Version=${BUILD_VERSION} -X github.com/cookieY/YearningX/backend/internal/platform/buildinfo.Commit=${BUILD_COMMIT} -X github.com/cookieY/YearningX/backend/internal/platform/assets.frontendDigest=${FRONTEND_DIST_SHA}" \
    -o /out/yearning ./cmd/yearning

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=builder /out/yearning /yearning
# Locked Frontend static assets (dedicated directory for host reverse-proxy or Go serving
# under read-only rootfs; hash pinned via label and asserted by post-build scan).
COPY frontend/dist /usr/share/yearning/frontend-dist
# SEC-0019: Required at runtime by the schema-migration Job — `migrate apply` points
# --migrations-dir and --catalog to these absolute in-image paths; build context is repo
# root (Dockerfile.dockerignore allowlist must permit db/migrations and
# api/contracts/postgresql-column-catalog.json).
COPY db/migrations /usr/share/yearning/db-migrations
COPY api/contracts/postgresql-column-catalog.json /usr/share/yearning/contracts/postgresql-column-catalog.json
ARG FRONTEND_DIST_SHA=missing
ARG VCS_REVISION=unknown
LABEL io.yearning.frontend-dist-sha256=${FRONTEND_DIST_SHA}
# SEC-0015: Audited HEAD pinned into image config via standard OCI label; post-build
# backend-release-image asserts this label == release HEAD (preventing manual builds
# that bypass Make from carrying a wrong or missing revision).
LABEL org.opencontainers.image.revision=${VCS_REVISION}
# Associate published GHCR package ghcr.io/cookiey/yearning with the public upstream repository.
LABEL org.opencontainers.image.source="https://github.com/cookieY/Yearning"
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/yearning"]
