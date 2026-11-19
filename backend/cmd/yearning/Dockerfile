# v4 backend Release image (BE-B13-RETIRE-LEGACY deliverable).
# Multi-stage build: builder uses the same build flags as Makefile backend-release
# (CGO_ENABLED=0, -tags nomsgpack to strip net/rpc linkage, buildinfo version injection);
# final stage uses distroless static nonroot to satisfy the K8s contract (run_as_non_root, read-only rootfs).
# Base images are pinned by digest (supply-chain hardening); update digests when upgrading Go.
# Formal Release image Digest/SBOM/multi-arch Index are produced by the Release pipeline (release-manifest contract).
FROM golang:1.25.0@sha256:5502b0e56fca23feba76dbc5387ba59c593c02ccc2f0f7355871ea9a0852cebe AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# Binary links only cmd/ and internal/ (verified via go list -deps); testdata/deploy do not enter build context.
COPY cmd ./cmd
COPY internal ./internal
ARG BUILD_VERSION=dev
ARG BUILD_COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -tags nomsgpack -trimpath \
    -ldflags "-s -w -X github.com/cookieY/YearningX/backend/internal/platform/buildinfo.Version=${BUILD_VERSION} -X github.com/cookieY/YearningX/backend/internal/platform/buildinfo.Commit=${BUILD_COMMIT}" \
    -o /out/yearning ./cmd/yearning

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=builder /out/yearning /yearning
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/yearning"]
