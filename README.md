# Snatcher

A self-hosted media download API for Apple Shortcuts, powered by yt-dlp and
FFmpeg. Share a URL, wait for the download, and save it to Photos or Files.

## Quick start

Clone this repository and set `SNATCHER_API_KEY` in [compose.yaml](compose.yaml).
Generate a key with:

```sh
openssl rand -hex 32
```

Build and start the single container:

```sh
docker compose up -d --build
curl http://127.0.0.1:8080/health
```

Keep the loopback port binding and use an HTTPS reverse proxy or Tailscale for
remote access.

Submit a URL to `POST /v1/snatch` with your `X-API-Key`. Poll the returned job
location, then download the completed file.

## Documentation

See the [wiki](https://github.com/mhmdxsadk/snatcher/wiki) for deployment,
configuration, the API, and Shortcut integration.
