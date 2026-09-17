# Snatcher

A lightweight, self-hosted API around Cobalt for Apple Shortcuts. Share a URL,
download the returned media, and save it to Photos or Files.

## Quick start

Requires Docker Compose and a working Cobalt instance. Put [compose.yaml](compose.yaml)
on your server and edit `COBALT` and `API_KEY` in its `environment` section.
No `.env` file is needed.

Generate a key with `openssl rand -hex 32`, or reuse your existing Shortcut key.
The Cobalt URL must be reachable from the container; `localhost` refers to the
container itself.

```sh
docker compose up -d
curl --fail http://127.0.0.1:8080/health
```

The image must first be published by the repository's GitHub Actions workflow.
The host port defaults to loopback. Replace `127.0.0.1` in `ports` with your
server's Tailscale IP for tailnet access, or use an HTTPS reverse proxy for public access. Point
your Shortcut at `/download` and send your key in the `X-API-Key` header.

Only `COBALT` and `API_KEY` are required. Add optional settings to `environment`
only when you need to override the defaults.

## Documentation

See the [GitHub Wiki](https://github.com/mhmdxsadk/snatcher/wiki) for deployment,
configuration, API, and development guides.
