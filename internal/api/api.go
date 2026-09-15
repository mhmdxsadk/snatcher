package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/mhmdxsadk/snatcher/internal/client"
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
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeError(w, 405, "method_not_allowed", "Use POST for this endpoint.")
			return
		}
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			writeError(w, 415, "invalid_content_type", "Send a JSON request body.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		defer r.Body.Close()
		var input *struct {
			URL string `json:"url"`
		}
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
				writeError(w, 413, "request_too_large", "The request body is too large.")
			} else {
				writeError(w, 400, "invalid_request", "Send one JSON object containing a URL.")
			}
			return
		}
		source, err := normalizeURL(input.URL)
		if err != nil {
			writeError(w, 400, "invalid_url", "Provide an absolute HTTP or HTTPS URL without credentials.")
			return
		}
		result, err := c.Resolve(r.Context(), source)
		if err != nil {
			upstreamError(w, err)
			return
		}
		if result.Status == "local-processing" {
			writeError(w, 422, "processing_required", "This media requires processing that Snatcher does not support yet.")
			return
		}
		items, err := mediaItems(result)
		if err != nil {
			writeError(w, 502, "invalid_upstream_response", "The media service returned an unusable result.")
			return
		}
		writeJSON(w, 200, struct {
			Status string `json:"status"`
			Items  []Item `json:"items"`
		}{"success", items})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, 404, "not_found", "Endpoint not found.")
	})
	return mux
}

func normalizeURL(raw string) (string, error) {
	u, err := parseURL(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	host := strings.ToLower(u.Hostname())
	instagram := host == "instagram.com" || strings.HasSuffix(host, ".instagram.com")
	tiktok := host == "tiktok.com" || strings.HasSuffix(host, ".tiktok.com")
	if instagram || tiktok {
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return "", err
		}
		changed := false
		for key := range query {
			lower := strings.ToLower(key)
			tracking := strings.HasPrefix(lower, "utm_") || lower == "fbclid"
			if instagram {
				tracking = tracking || lower == "igsh" || lower == "igshid" || lower == "igshidp"
			}
			if tiktok {
				tracking = tracking || lower == "_t" || lower == "_r" || lower == "is_from_webapp" || lower == "sender_device" || lower == "sender_web_id"
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
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || strings.ContainsAny(u.Host, " \t\r\n") {
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
		items = []Item{{URL: result.URL, Filename: filename, Type: kind}}
	case "picker":
		// Background slideshow audio is not a separate Photos item.
		for _, entry := range result.Picker {
			switch entry.Type {
			case "photo", "video", "gif":
				items = append(items, Item{URL: entry.URL, Type: entry.Type})
			default:
				return nil, fmt.Errorf("unsupported picker type")
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
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()):
		writeError(w, 504, "upstream_timeout", "The media service took too long to respond.")
	case errors.As(err, &apiErr):
		if apiErr.HTTPStatus == 429 {
			writeError(w, 503, "upstream_busy", "The media service is busy. Try again later.")
		} else if strings.HasSuffix(apiErr.Code, "api.fetch.empty") {
			writeError(w, 422, "media_unavailable", "The media service could not retrieve this post.")
		} else {
			writeError(w, 502, "upstream_error", "The media service could not resolve this URL.")
		}
	case errors.As(err, &httpErr) && httpErr.StatusCode == 429:
		writeError(w, 503, "upstream_busy", "The media service is busy. Try again later.")
	default:
		writeError(w, 502, "upstream_error", "The media service could not complete the request.")
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"status": "error",
		"error":  map[string]string{"code": code, "message": message},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
