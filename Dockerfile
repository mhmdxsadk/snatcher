# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine AS build
WORKDIR /src

COPY go.mod ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/snatcher ./cmd/snatcher

FROM node:26-bookworm-slim
ARG YTDLP_VERSION=2026.8.19
RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-venv ffmpeg ca-certificates \
    && python3 -m venv /opt/yt-dlp \
    && /opt/yt-dlp/bin/pip install --no-cache-dir "yt-dlp[default,curl-cffi]==${YTDLP_VERSION}" \
    && /opt/yt-dlp/bin/yt-dlp --ignore-config --list-impersonate-targets > /tmp/impersonate-targets \
    && grep -Eq 'curl_cffi$' /tmp/impersonate-targets \
    && rm /tmp/impersonate-targets \
    && rm -rf /var/lib/apt/lists/*
ENV PATH="/opt/yt-dlp/bin:${PATH}" HOME=/tmp

COPY --from=build /out/snatcher /snatcher
USER 65532:65532
ENV LISTEN=0.0.0.0:8080
EXPOSE 8080
ENTRYPOINT ["/snatcher"]
