# syntax=docker/dockerfile:1.7

############################
# Global build args (visible to all stages)
############################
ARG GO_VERSION=1.22.6
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

############################
# Build stage
############################
# Pin the Go toolchain by tag so `GO_VERSION` stays configurable without
# breaking the FROM line. Distroless stays digest-pinned for supply-chain safety.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
WORKDIR /src

# Re-import global args into this stage's scope
ARG VERSION
ARG COMMIT
ARG DATE

# The project uses the pure-Go SQLite driver, so CGO can stay disabled to
# produce a portable static binary.
ENV GOTOOLCHAIN=auto \
    CGO_ENABLED=0

ARG TARGETOS
ARG TARGETARCH

RUN apt-get update \
 && apt-get install -y --no-install-recommends build-essential ca-certificates \
 && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# Ensure optional dirs always exist so runtime COPY never fails
RUN mkdir -p /src/data /src/docs

RUN --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
      -o /out/server ./cmd/server

############################
# Runtime stage
############################
FROM gcr.io/distroless/base-debian12@sha256:4f6e739881403e7d50f52a4e574c4e3c88266031fd555303ee2f1ba262523d6a
WORKDIR /app

# Re-import args so LABEL can use them
ARG VERSION
ARG COMMIT
ARG DATE

LABEL org.opencontainers.image.title="go-chat-backend" \
      org.opencontainers.image.description="Chat-based Q&A API over Markdown data" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${DATE}" \
      org.opencontainers.image.source="https://github.com/tbourn/go-chat-backend" \
      org.opencontainers.image.licenses="MIT"

USER 65532:65532
VOLUME ["/data"]

# Optional --chown (not strictly needed, but harmless)
COPY --from=build --chown=65532:65532 /out/server /app/server
COPY --from=build --chown=65532:65532 /src/data /app/data
COPY --from=build --chown=65532:65532 /src/docs /app/docs

ENV PORT=8080 \
    DB_PATH=/data/app.db \
    DATA_MD=/app/data/data.md \
    THRESHOLD=0.32 \
    LOG_LEVEL=info \
    GIN_MODE=release \
    RATE_RPS=5 \
    RATE_BURST=10

EXPOSE 8080
STOPSIGNAL SIGTERM
ENTRYPOINT ["/app/server"]
