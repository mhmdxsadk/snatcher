# Snatcher

A lightweight, self-hosted Go API around Cobalt for Apple Shortcuts. Snatcher
normalizes shared URLs and turns Cobalt responses into an ordered list of media
items, so a Shortcut can download each item and save it to Photos.

## Run

Requires Go 1.27.1 or newer and a working Cobalt instance. Keep the existing
waxseal bridge configured on the Cobalt side; Snatcher only talks to Cobalt.

```sh
COBALT=http://localhost:9000 LISTEN=127.0.0.1:8080 go run ./cmd/snatcher
```

| Variable | Meaning | Default |
| --- | --- | --- |
| `COBALT` | Required Cobalt HTTP(S) base URL; reverse proxy paths are supported | None |
| `LISTEN` | Snatcher host and port | `127.0.0.1:8080` |

Configuration comes from process environment variables. `.env.example` documents
them; Snatcher does not load `.env` files automatically.

For access from an iPhone, use a reachable host address or reverse proxy. The
Cobalt media URLs returned by Snatcher must also be reachable from the phone.
There is currently no authentication in Snatcher; deploy it on a trusted network
or behind an authenticated proxy.

## API

### `GET /health`

Returns HTTP 200 with `{"status":"ok"}`. This checks that Snatcher is running;
it does not contact Cobalt. `HEAD` is also supported.

### `POST /download`

```sh
curl http://127.0.0.1:8080/download \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://www.youtube.com/watch?v=VIDEO_ID"}'
```

Send one JSON object with a `url` field. Requests are limited to 16 KiB and unknown
fields are rejected. URLs must use HTTP or HTTPS and contain no credentials.
Snatcher trims surrounding whitespace and removes known Instagram and TikTok
tracking parameters while preserving selection parameters such as `img_index`.

Success (HTTP 200):

```json
{
  "status": "success",
  "items": [
    {
      "url": "https://cobalt.example/tunnel?id=...",
      "filename": "video.mp4",
      "type": "video"
    }
  ]
}
```

Single downloads and galleries use the same `items` array. Gallery order is
preserved. `filename` is optional; `type` is `photo`, `video`, `gif`, `audio`, or
`unknown`. For single files, type is inferred from the filename or URL extension.
It is a hint, not a guarantee of Photos compatibility.

The endpoint resolves media URLs; it does not stream file bytes. Download returned
URLs promptly because the upstream links may expire. Gallery background audio is
omitted. Media requiring local processing returns `processing_required`.

Errors use a consistent JSON envelope:

```json
{
  "status": "error",
  "error": {
    "code": "invalid_url",
    "message": "Provide an absolute HTTP or HTTPS URL without credentials."
  }
}
```

| HTTP status | Error codes |
| --- | --- |
| 400 | `invalid_request`, `invalid_url` |
| 404 | `not_found` |
| 405 | `method_not_allowed` |
| 413 | `request_too_large` |
| 415 | `invalid_content_type` |
| 422 | `media_unavailable`, `processing_required` |
| 502 | `upstream_error`, `invalid_upstream_response` |
| 503 | `upstream_busy` |
| 504 | `upstream_timeout` |

Cobalt requests time out after 30 seconds. JSON responses use `Cache-Control:
no-store`.

## Apple Shortcuts flow

1. Receive a shared URL.
2. Use **Get Contents of URL** to POST to Snatcher's `/download` endpoint with a
   JSON body containing `url`.
3. Check the response's `status`. If it is `error`, show `error.message` and stop.
4. Repeat over `items`, fetch each item's `url`, and save compatible media to
   Photos. Route audio or unsupported file types to Files as needed.

## Development

```sh
go test -race ./...
go vet ./...
go build ./cmd/snatcher
```

The integration tests use a local mock Cobalt server. They cover the request
contract, single files, galleries, tracking cleanup, validation, and upstream
failures. Real Cobalt downloads and saving to Photos need an end-to-end check
against your deployment and device.
