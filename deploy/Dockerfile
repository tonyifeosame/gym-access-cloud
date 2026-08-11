# Production image for the Access Terminal Cloud API.
#
# Separate from the Dockerfile at the repository root, which is the bench/dev
# one. The differences are all things that matter only when the thing is
# exposed to a network:
#
#   * pinned base image digests are NOT used here, but the tags are pinned to a
#     minor version -- `latest` on the runtime stage meant a rebuild months
#     apart could produce two different images from one commit.
#   * the binary runs as a NON-ROOT user. The root image ran the API as uid 0
#     with a writable WORKDIR, so a write primitive anywhere in the process was
#     a write to the image.
#   * build metadata is stamped into the binary, so a running container can be
#     matched to a commit without guessing.
#   * a HEALTHCHECK, so an orchestrator restarts a process that is up but not
#     serving.
#
# Build:
#   docker build -f deploy/Dockerfile \
#     --build-arg VERSION="$(git describe --tags --always --dirty)" \
#     --build-arg COMMIT="$(git rev-parse --short HEAD)" \
#     -t access-terminal-api:latest .

# --- build stage ------------------------------------------------------------
FROM golang:1.21-alpine AS builder

WORKDIR /src

# Dependencies first, so a source-only change does not re-download the module
# cache on every build.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown

# CGO off so the result is a static binary that runs on scratch/alpine without
# a libc. -trimpath keeps build-host paths out of the binary.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/access-terminal-cloud-api .

# --- runtime stage ----------------------------------------------------------
FROM alpine:3.19

# ca-certificates: the API itself makes no outbound TLS calls today, but a
# future one (webhooks, object storage for firmware) would fail obscurely
# without them. wget is used by the healthcheck below and is already in busybox.
RUN apk --no-cache add ca-certificates tzdata \
 && addgroup -S app -g 10001 \
 && adduser -S app -G app -u 10001

# Owned by root, readable by all, writable by nobody. The process needs no
# write access anywhere: it keeps no state on disk.
COPY --from=builder --chown=root:root --chmod=0755 \
     /out/access-terminal-cloud-api /usr/local/bin/access-terminal-cloud-api

USER 10001:10001
WORKDIR /

EXPOSE 8080

# Liveness only -- deliberately NOT /health/ready. Readiness fails when the
# database is unreachable, and restarting every API container during a database
# blip turns one outage into two. See docs/operations.md.
#
# ${PORT:-8080} rather than a literal: a platform that starts this image chooses
# the port and injects PORT, and the process binds what it is given. Hard-coding
# 8080 here made the healthcheck probe a port nothing was listening on and
# report the container unhealthy while it was serving normally. Compose and
# systemd set no PORT, so they keep 8080.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:${PORT:-8080}/health/live" >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/access-terminal-cloud-api"]
