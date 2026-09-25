package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	mux.HandleFunc("GET /"+version.API+"/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
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
				query := url.Values{"exp": {expiry}, "sig": {downloadSignature(key, itemID, expiry)}}
				link.RawQuery = query.Encode()
				response.Items = append(response.Items, Item{URL: link.String(), Filename: filepath.Base(path), Type: download.MediaType(path, job.MediaType)})
			}
		}
		writeJSON(w, http.StatusOK, response)
	})
	mux.HandleFunc("POST /"+version.API+"/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		job, ok := d.Cancel(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "Job not found.")
			return
		}
		writeJSON(w, http.StatusOK, job)
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
		if err != nil || time.Now().Unix() >= timestamp || !hmac.Equal([]byte(sig), []byte(downloadSignature(key, itemID, expiry))) {
			writeError(w, http.StatusForbidden, "invalid_link", "Download link is invalid or expired.")
			return
		}
		job, ok := d.Get(id)
		if !ok || job.Status != download.StatusCompleted || index >= len(job.Files) {
			writeError(w, http.StatusNotFound, "not_found", "Download not found.")
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
		http.NewResponseController(w).SetWriteDeadline(time.Time{})
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(job.Files[index])}))
		http.ServeContent(w, r, filepath.Base(job.Files[index]), info.ModTime(), file)
	}
	mux.HandleFunc("/download/{id}", serveDownload)
	mux.HandleFunc("/download/{id}/{index}", serveDownload)
}
