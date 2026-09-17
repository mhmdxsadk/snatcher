# Snatcher

A lightweight, self-hosted API around Cobalt for Apple Shortcuts. Share a URL,
download the returned media, and save it to Photos or Files.

## Quick start

Requires Docker Compose and a working Cobalt instance. Put [compose.yaml](compose.yaml)
on your server and edit `COBALT` and `API_KEY` in its `environment` section.
No `.env` file is needed.

> [!WARNING]
> Only use a Cobalt instance you host or have permission to use.

Generate a key with `openssl rand -hex 32`.
The Cobalt URL must be reachable from Snatcher.

```sh
docker compose up -d
curl --fail http://127.0.0.1:8080/health
```

The host port defaults to loopback. Replace `127.0.0.1` in `ports` with your
server's Tailscale IP for tailnet access, or use an HTTPS reverse proxy for
public access. Point your Shortcut at `https://YOUR_SNATCHER_HOST/v1/snatcher`.
For example:

```sh
curl --fail-with-body 'https://YOUR_SNATCHER_HOST/v1/snatcher' \
  -H 'Content-Type: application/json' \
  -H 'X-API-Key: YOUR_API_KEY' \
  --data '{"url":"https://www.instagram.com/reel/POST_ID/"}'
```

Replace the server URL, API key, and media URL with your own values. See the
wiki for additional download options and networking details.

## Documentation

See the [wiki](https://github.com/mhmdxsadk/snatcher/wiki) for deployment,
configuration, API, and development guides.
