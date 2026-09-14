# Snatcher

A lightweight Go backend for an Apple Shortcut that downloads social media through a self-hosted Cobalt instance.

This first step provides a health endpoint, environment configuration, and an internal Cobalt client. It uses only the Go standard library.

## Run locally

Use Go 1.27.1 or later. With Tailscale connected to your Cobalt server:

```sh
export COBALT=http://100.118.115.91:9000
go run ./cmd/snatcher
```

In another terminal:

```sh
curl -i http://127.0.0.1:8080/health
```

Expected: HTTP 200 with `{"status":"ok"}`. Health is a liveness check; it does not contact Cobalt. Stop with Ctrl-C.

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `COBALT` | Yes | None | Absolute HTTP(S) base URL of your Cobalt instance |
| `LISTEN` | No | `127.0.0.1:8080` | Snatcher's listening address |

Configuration is validated at startup. `.env.example` documents the variables; the application does not automatically load `.env` files. Local `.env` files are ignored by Git.

## Verify

```sh
go vet ./...
go build -o bin/snatcher ./cmd/snatcher
```

There are currently no automated tests. Configuration and API behavior can be checked manually using the startup and health commands above.

## Current boundaries

- `cmd/snatcher`: startup, server timeouts, graceful shutdown.
- `internal/config`: environment loading and validation.
- `internal/api`: `GET /health` (also supports HEAD).
- `internal/client`: `New(baseURL)` and `Resolve(ctx, sourceURL)` for Cobalt's `POST /` protocol.

The client sets JSON headers, requests `alwaysProxy: true` and `localProcessing: "disabled"`, uses a 30-second timeout, and caps response bodies at 1 MiB. It parses tunnel, redirect, picker, and local-processing instructions, and returns typed upstream API/HTTP errors. Local-processing results still require backend processing; parsing them does not produce a finished media file. Variant-specific audio/output details are preserved as JSON until that processing is implemented.

The client is not yet wired into an HTTP download endpoint. Cobalt's response types are internal and do not define Snatcher's public API. Source URLs are passed unchanged for now, including YouTube query parameters. Service-aware normalization, a normalized media response, download handling, fallbacks, authentication, and rate limiting are future steps. YouTube behavior has not been debugged here.

## Deployment direction

Eventually: public HTTPS → authenticated Snatcher → private Cobalt. When containerized together, use `COBALT=http://cobalt:9000` on a shared Docker network, with **no published Cobalt port**. Snatcher can use `LISTEN=:8080` inside its container. Direct Cobalt debugging can use SSH forwarding.

The current localhost default is intended for development. Public sharing should follow the authentication and rate-limiting work.
