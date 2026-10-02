package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mhmdxsadk/snatcher/internal/download"
	"github.com/mhmdxsadk/snatcher/internal/version"
)

func downloadSignature(key, id, expiry string) string {
	h := hmac.New(sha256.New, []byte(key))
	h.Write([]byte("download:" + id + ":" + expiry))
	return hex.EncodeToString(h.Sum(nil))
}
func registerJobs(mux *http.ServeMux, d *download.Manager, key string) {
	transfers := make(chan struct{}, 4)
	authorize := func(w http.ResponseWriter, r *http.Request) bool {
		job, ok := d.Get(r.PathValue("id"))
		headers := r.Header.Values("X-Job-Token")
		if !ok || len(headers) != 1 || subtle.ConstantTimeCompare([]byte(headers[0]), []byte(job.Token)) != 1 {
			writeError(w, http.StatusNotFound, "not_found", "Job not found or expired.")
			return false
		}
		return true
	}
	mux.HandleFunc("GET /"+version.API+"/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r) {
			return
		}
		job, ok := d.Get(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "Job not found or expired.")
			return
		}
		w.Header().Set("Retry-After", "5")
		response := struct {
			download.Job
			Items []Item `json:"items,omitempty"`
		}{Job: job}
		if job.Status == download.StatusCompleted {
			expiry := strconv.FormatInt(job.Expires.Unix(), 10)
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			// The security middleware removes untrusted or invalid forwarded schemes.
			if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" || proto == "http" {
				scheme = proto
			}
			for index, path := range job.Files {
				itemID := job.ID
				if index > 0 {
					itemID += "/" + strconv.Itoa(index)
				}
				link := url.URL{Scheme: scheme, Host: r.Host, Path: "/download/" + itemID}
				query := url.Values{"exp": {expiry}, "sig": {downloadSignature(job.Token, itemID, expiry)}}
				link.RawQuery = query.Encode()
				response.Items = append(response.Items, Item{URL: link.String(), Filename: filepath.Base(path), Type: download.MediaType(path, job.MediaType)})
			}
		}
		writeJSON(w, http.StatusOK, response)
	})
	mux.HandleFunc("POST /"+version.API+"/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r) {
			return
		}
		job, ok := d.Cancel(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "Job not found.")
			return
		}
		writeJSON(w, http.StatusOK, job)
	})
	mux.HandleFunc("DELETE /"+version.API+"/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r) {
			return
		}
		err := d.Delete(r.PathValue("id"))
		switch {
		case errors.Is(err, download.ErrActive):
			writeError(w, http.StatusConflict, "job_active", "Cancel the job and wait for it to stop before deleting.")
		case errors.Is(err, os.ErrNotExist):
			writeError(w, http.StatusNotFound, "not_found", "Job not found or expired.")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "delete_failed", "Unable to delete job.")
		default:
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNoContent)
		}
	})
	serveDownload := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET or HEAD.")
			return
		}
		id := r.PathValue("id")
		itemID := id
		index := 0
		if raw := r.PathValue("index"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || strconv.Itoa(parsed) != raw {
				writeError(w, http.StatusNotFound, "not_found", "Download not found.")
				return
			}
			index = parsed
			itemID += "/" + raw
		}
		expiry := r.URL.Query().Get("exp")
		sig := r.URL.Query().Get("sig")
		timestamp, err := strconv.ParseInt(expiry, 10, 64)
		job, ok := d.Get(id)
		if !ok || err != nil || time.Now().Unix() >= timestamp || timestamp > job.Expires.Unix() ||
			!hmac.Equal([]byte(sig), []byte(downloadSignature(job.Token, itemID, expiry))) {
			writeError(w, http.StatusForbidden, "invalid_link", "Download link is invalid or expired.")
			return
		}
		if job.Status != download.StatusCompleted || index >= len(job.Files) {
			writeError(w, http.StatusNotFound, "not_found", "Download not found.")
			return
		}
		select {
		case transfers <- struct{}{}:
			defer func() { <-transfers }()
		default:
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusTooManyRequests, "download_busy", "Download capacity reached. Try again shortly.")
			return
		}
		file, err := os.Open(job.Files[index])
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "Download not found.")
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read_failed", "Cannot read download.")
			return
		}
		// Bound both stalled writes and total lifetime, including link expiry.
		deadline := time.Now().Add(15 * time.Minute)
		if expiry := time.Unix(timestamp, 0); expiry.Before(deadline) {
			deadline = expiry
		}
		writer := &transferWriter{ResponseWriter: w, deadline: deadline}
		if err := writer.refreshDeadline(); err != nil {
			writeError(w, http.StatusInternalServerError, "transfer_failed", "Unable to start download.")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(job.Files[index])}))
		http.ServeContent(writer, r, filepath.Base(job.Files[index]), info.ModTime(), file)
	}
	mux.HandleFunc("/download/{id}", serveDownload)
	mux.HandleFunc("/download/{id}/{index}", serveDownload)
}

// transferWriter deliberately does not implement ReaderFrom: every write must
// refresh the idle deadline, including transfers that could otherwise use sendfile.
type transferWriter struct {
	http.ResponseWriter
	deadline time.Time
}

func (w *transferWriter) refreshDeadline() error {
	deadline := time.Now().Add(30 * time.Second)
	if w.deadline.Before(deadline) {
		deadline = w.deadline
	}
	err := http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline)
	// In-memory test recorders do not implement network deadlines.
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func (w *transferWriter) Write(p []byte) (int, error) {
	if !time.Now().Before(w.deadline) {
		return 0, os.ErrDeadlineExceeded
	}
	if err := w.refreshDeadline(); err != nil {
		return 0, err
	}
	return w.ResponseWriter.Write(p)
}
