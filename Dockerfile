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

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/snatcher /snatcher
USER 65532:65532
ENV LISTEN=0.0.0.0:8080
EXPOSE 8080
ENTRYPOINT ["/snatcher"]
