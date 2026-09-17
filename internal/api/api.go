package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/mhmdxsadk/snatcher/internal/client"
	"github.com/mhmdxsadk/snatcher/internal/version"
)

type Item struct {
	URL      string `json:"url"`
	Filename string `json:"filename,omitempty"`
	Type     string `json:"type"`
}

func NewHandler(c *client.Client) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET for this endpoint.")
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/"+version.API+"/snatch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use POST for this endpoint.")
			return
		}

		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Send a JSON request body.")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		defer r.Body.Close()

		var input *downloadRequest

		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()

		err = decoder.Decode(&input)

		if err == nil {
			var extra any
			err = decoder.Decode(&extra)

			if err == io.EOF {
				err = nil
			} else if err == nil {
				err = errors.New("multiple JSON values")
			}
		}

		if err == nil && input == nil {
			err = errors.New("expected JSON object")
		}

		if err != nil {
			var limit *http.MaxBytesError

			if errors.As(err, &limit) {
				writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "The request body is too large.")
			} else {
				writeError(w, http.StatusBadRequest, "invalid_request", "Send one JSON object containing a URL.")
			}

			return
		}

		source, err := normalizeURL(input.URL)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_url", "Provide an absolute HTTP or HTTPS URL without credentials.")
			return
		}

		options, err := input.options()
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_options", err.Error())
			return
		}

		result, err := c.Resolve(r.Context(), source, options)
		if err != nil {
			upstreamError(w, err)
			return
		}

		if result.Status == "local-processing" {
			writeError(w, http.StatusUnprocessableEntity, "processing_required", "This media requires processing that Snatcher does not support yet.")
			return
		}

		if options.Mode == "audio" && result.Status == "picker" {
			var audioURL string

			if len(result.Audio) != 0 {
				if err := json.Unmarshal(result.Audio, &audioURL); err != nil {
					writeError(w, http.StatusBadGateway, "invalid_upstream_response", "The media service returned an unusable result.")
					return
				}
			}

			if audioURL == "" {
				writeError(w, http.StatusUnprocessableEntity, "audio_unavailable", "This gallery has no downloadable audio.")
				return
			}

			result = &client.Response{
				Status:   "redirect",
				URL:      audioURL,
				Filename: result.AudioFilename,
			}
		}

		items, err := mediaItems(result)
		if err != nil {
			writeError(w, http.StatusBadGateway, "invalid_upstream_response", "The media service returned an unusable result.")
			return
		}

		for i := range items {
			rewritten, err := rewriteTunnelURL(c, r, items[i].URL)
			if err != nil {
				writeError(w, http.StatusBadGateway, "invalid_upstream_response", "The media service returned an unusable result.")
				return
			}

			items[i].URL = rewritten
			if options.Mode == "audio" {
				items[i].Type = "audio"
			}
		}

		writeJSON(w, http.StatusOK, struct {
			Status string `json:"status"`
			Items  []Item `json:"items"`
		}{
			Status: "success",
			Items:  items,
		})
	})

	mux.HandleFunc("/tunnel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET for this endpoint.")
			return
		}

		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_tunnel", "The download URL is invalid.")
			return
		}

		// Cobalt validates the signature and expiry; reject incomplete URLs here.
		for _, key := range []string{"id", "exp", "sig"} {
			if query.Get(key) == "" {
				writeError(w, http.StatusBadRequest, "invalid_tunnel", "The download URL is invalid.")
				return
			}
		}

		// Transfers can outlast the server's deadline for ordinary API responses.
		if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
			panic(http.ErrAbortHandler)
		}

		resp, err := c.Tunnel(r.Context(), r.Method, query, r.Header.Get("Range"))
		if err != nil {
			upstreamError(w, err)
			return
		}
		defer resp.Body.Close()

		for _, name := range []string{
			"Content-Type", "Content-Length", "Content-Disposition", "Accept-Ranges",
			"Content-Range", "Content-Encoding", "ETag", "Last-Modified",
		} {
			if value := resp.Header.Get(name); value != "" {
				w.Header().Set(name, value)
			}
		}

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		w.WriteHeader(resp.StatusCode)

		if r.Method == http.MethodHead {
			return
		}

		if _, err := io.Copy(w, resp.Body); err != nil {
			// Abort the response so a truncated download cannot appear complete.
			panic(http.ErrAbortHandler)
		}
	})

	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET or HEAD for this endpoint.")
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			API     string `json:"api"`
		}{Name: "Snatcher", Version: version.Release, API: version.API})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Endpoint not found.")
	})

	return mux
}

// rewriteTunnelURL exposes configured Cobalt tunnels through Snatcher.
func rewriteTunnelURL(c *client.Client, r *http.Request, raw string) (string, error) {
	u, err := parseURL(raw)
	if err != nil {
		return "", err
	}

	if !c.IsTunnelURL(u) {
		return raw, nil
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}

	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		// X-Forwarded-Proto can theoretically contain multiple values.
		if i := strings.IndexByte(forwarded, ','); i >= 0 {
			forwarded = forwarded[:i]
		}

		forwarded = strings.TrimSpace(forwarded)

		if forwarded == "http" || forwarded == "https" {
			scheme = forwarded
		}
	}

	if r.Host == "" {
		return "", errors.New("request has no host")
	}

	public := &url.URL{
		Scheme:   scheme,
		Host:     r.Host,
		Path:     "/tunnel",
		RawQuery: u.RawQuery,
	}

	return public.String(), nil
}

func normalizeURL(raw string) (string, error) {
	u, err := parseURL(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}

	host := strings.ToLower(u.Hostname())

	instagram :=
		host == "instagram.com" ||
			strings.HasSuffix(host, ".instagram.com")

	tiktok :=
		host == "tiktok.com" ||
			strings.HasSuffix(host, ".tiktok.com")

	if instagram || tiktok {
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return "", err
		}

		changed := false

		for key := range query {
			lower := strings.ToLower(key)

			tracking :=
				strings.HasPrefix(lower, "utm_") ||
					lower == "fbclid"

			if instagram {
				tracking =
					tracking ||
						lower == "igsh" ||
						lower == "igshid" ||
						lower == "igshidp"
			}

			if tiktok {
				tracking =
					tracking ||
						lower == "_t" ||
						lower == "_r" ||
						lower == "is_from_webapp" ||
						lower == "sender_device" ||
						lower == "sender_web_id"
			}

			if tracking {
				query.Del(key)
				changed = true
			}
		}

		if changed {
			u.RawQuery = query.Encode()
			u.ForceQuery = false
		}
	}

	return u.String(), nil
}

func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}

	if (u.Scheme != "http" && u.Scheme != "https") ||
		u.Hostname() == "" ||
		u.User != nil ||
		strings.ContainsAny(u.Host, " \t\r\n") {
		return nil, errors.New("invalid HTTP URL")
	}

	return u, nil
}

func mediaItems(result *client.Response) ([]Item, error) {
	var items []Item

	switch result.Status {
	case "tunnel", "redirect":
		filename := result.Filename

		if filename != "" {
			filename = path.Base(strings.ReplaceAll(filename, "\\", "/"))
		}

		kind := mediaType(filename)

		if kind == "unknown" {
			u, err := parseURL(result.URL)
			if err != nil {
				return nil, err
			}

			kind = mediaType(u.Path)
		}

		items = []Item{{
			URL:      result.URL,
			Filename: filename,
			Type:     kind,
		}}

	case "picker":
		// Background slideshow audio is not a separate Photos item.
		for _, entry := range result.Picker {
			switch entry.Type {
			case "photo", "video", "gif":
				items = append(items, Item{
					URL:  entry.URL,
					Type: entry.Type,
				})

			default:
				return nil, errors.New("unsupported picker type")
			}
		}

	default:
		return nil, errors.New("unsupported result")
	}

	if len(items) == 0 {
		return nil, errors.New("empty result")
	}

	for _, item := range items {
		if _, err := parseURL(item.URL); err != nil {
			return nil, err
		}
	}

	return items, nil
}

// File extensions are hints; actual Photos compatibility needs a device check.
func mediaType(filename string) string {
	switch strings.ToLower(path.Ext(filename)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".heic", ".avif":
		return "photo"

	case ".mp4", ".mov", ".m4v", ".webm", ".mkv":
		return "video"

	case ".gif":
		return "gif"

	case ".mp3", ".m4a", ".wav", ".ogg", ".opus":
		return "audio"

	default:
		return "unknown"
	}
}

func upstreamError(w http.ResponseWriter, err error) {
	var network net.Error
	var apiErr *client.APIError
	var httpErr *client.HTTPError

	switch {
	case errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &network) && network.Timeout()):
		writeError(w, http.StatusGatewayTimeout, "upstream_timeout", "The media service took too long to respond.")

	case errors.As(err, &apiErr):
		if apiErr.HTTPStatus == http.StatusTooManyRequests {
			writeError(w, http.StatusServiceUnavailable, "upstream_busy", "The media service is busy. Try again later.")
		} else if strings.HasSuffix(apiErr.Code, "api.fetch.empty") {
			writeError(w, http.StatusUnprocessableEntity, "media_unavailable", "The media service could not retrieve this post.")
		} else {
			writeError(w, http.StatusBadGateway, "upstream_error", "The media service could not resolve this URL.")
		}

	case errors.As(err, &httpErr) &&
		httpErr.StatusCode == http.StatusTooManyRequests:
		writeError(w, http.StatusServiceUnavailable, "upstream_busy", "The media service is busy. Try again later.")

	default:
		writeError(w, http.StatusBadGateway, "upstream_error", "The media service could not complete the request.")
	}
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]any{
		"status": "error",
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
