# Snatcher

A lightweight, self-hosted API around Cobalt for Apple Shortcuts. Share a URL,
download the returned media, and save it to Photos or Files.

## Quick start

Requires Docker Compose and a working Cobalt instance. Put [compose.yaml](compose.yaml)
on your server and set `COBALT` and `API_KEY` in its `environment` section.

<br>

> [!WARNING]
> Only use a Cobalt instance you host or have permission to use.

<br>

Set `API_KEY` to a random string of at least 32 characters. You can generate one with:

```sh
openssl rand -hex 32
```

The Cobalt API must be reachable from the Snatcher container.

```sh
docker compose up -d
curl --fail http://127.0.0.1:8080/health
```

Keep `127.0.0.1` in `ports` for host-only access. For public access, use
Cloudflare Tunnel (recommended) or another HTTPS reverse proxy. The port mapping
can stay unchanged when the tunnel or proxy connects to Snatcher through the host.

For direct tailnet access, replace `127.0.0.1` with your server's Tailscale IP.

Point your Shortcut at `https://snatcher.example.com/v1/snatcher`.
Replace `snatcher.example.com` with your domain.

```sh
curl --fail-with-body 'https://snatcher.example.com/v1/snatcher' \
  -H 'Content-Type: application/json' \
  -H 'X-API-Key: YOUR_API_KEY' \
  --data '{"url":"https://www.instagram.com/reel/POST_ID/"}'
```

Replace the server URL, API key, and media URL with your own values. See the
wiki for additional download options and networking details.

## Documentation

See the [wiki](https://github.com/mhmdxsadk/snatcher/wiki) for deployment,
configuration, API, and development guides.
