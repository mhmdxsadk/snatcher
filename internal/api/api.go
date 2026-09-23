package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/mhmdxsadk/snatcher/internal/download"
	"github.com/mhmdxsadk/snatcher/internal/version"
)

type Item struct {
	URL      string `json:"url"`
	Filename string `json:"filename,omitempty"`
	Type     string `json:"type"`
}

func NewHandler(d *download.Manager, key string) http.Handler {
	mux := http.NewServeMux()
	registerJobs(mux, d, key)

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

		job, err := d.Submit(download.Request{URL: source, Quality: options.Quality, Mode: options.Mode, AudioFormat: options.AudioFormat})
		if err != nil {
			if !errors.Is(err, download.ErrFull) {
				writeError(w, http.StatusInternalServerError, "job_failed", "Unable to create download job.")
				return
			}
			w.Header().Set("Retry-After", "10")
			writeError(w, http.StatusTooManyRequests, "queue_full", "Download capacity reached. Try again later.")
			return
		}
		w.Header().Set("Location", "/"+version.API+"/jobs/"+job.ID)
		w.Header().Set("Retry-After", "5")
		writeJSON(w, http.StatusAccepted, job)
		return
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
