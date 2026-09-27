package download

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Cobalt is a fallback API client, not a platform-specific extractor.
type Cobalt struct{ Endpoint, APIKey string }
type cobaltItem struct {
	URL      string `json:"url"`
	Type     string `json:"type"`
	Filename string `json:"filename"`
}
type cobaltResponse struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
	Status   string       `json:"status"`
	URL      string       `json:"url"`
	Filename string       `json:"filename"`
	Picker   []cobaltItem `json:"picker"`
}

func (c *Cobalt) Run(ctx context.Context, r Request, dir string) ([]string, error) {
	ctx, stop := watchSize(ctx, dir)
	defer stop()
	body, err := json.Marshal(map[string]any{
		"url": r.URL, "videoQuality": r.Quality, "downloadMode": r.Mode,
		"audioFormat": r.AudioFormat, "alwaysProxy": true, "localProcessing": "disabled",
		"youtubeVideoCodec": "h264", "youtubeVideoContainer": "mp4", "convertGif": false,
	})
	if err != nil {
		return nil, err
	}
	resolveCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(resolveCtx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid Cobalt endpoint")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Api-Key "+c.APIKey)
	}
	// API credentials must never be sent to a redirected endpoint.
	api := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := api.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		return nil, errors.New("Cobalt API request failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("invalid Cobalt response size")
	}
	var result cobaltResponse
	if json.Unmarshal(data, &result) != nil {
		return nil, fmt.Errorf("invalid Cobalt response (HTTP %d)", response.StatusCode)
	}
	if response.StatusCode != http.StatusOK || result.Status == "error" {
		return nil, fmt.Errorf("Cobalt API returned HTTP %d (%s)", response.StatusCode, diagnostic(result.Error.Code))
	}
	var items []cobaltItem
	switch result.Status {
	case "picker":
		if r.Mode == "audio" {
			return nil, errors.New("Cobalt returned a gallery for an audio request")
		}
		items = result.Picker
	case "redirect", "tunnel":
		items = []cobaltItem{{URL: result.URL, Filename: result.Filename}}
	default:
		return nil, errors.New("Cobalt could not resolve the media")
	}
	if len(items) == 0 || len(items) > maxJobFiles {
		return nil, errors.New("invalid Cobalt item count")
	}
	var files []string
	remaining := maxJobBytes
	for i, item := range items {
		if item.Type != "" && item.Type != "photo" && item.Type != "video" && item.Type != "gif" {
			return nil, errors.New("invalid Cobalt media type")
		}
		file, size, err := fetchCobaltMedia(ctx, item, r, dir, i, remaining)
		if err != nil {
			return nil, err
		}
		remaining -= size
		if r.Mode != "audio" && MediaType(file, "video") != "photo" {
			file, err = compatibleVideo(ctx, file, r.Mode == "mute")
			if err != nil {
				return nil, err
			}
		}
		if r.Mode == "audio" || MediaType(file, "video") == "photo" {
			if err := validateMedia(ctx, file, r.Mode); err != nil {
				return nil, err
			}
		}
		files = append(files, file)
	}
	return files, nil
}

func fetchCobaltMedia(ctx context.Context, item cobaltItem, r Request, dir string, index int, budget int64) (string, int64, error) {
	u, err := url.Parse(item.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", 0, errors.New("invalid Cobalt media URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", 0, errors.New("invalid Cobalt media request")
	}
	// Media requests never inherit API credentials; use GET rather than HEAD.
	client := &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || (req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.User != nil {
			return errors.New("invalid media redirect")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, context.Cause(ctx)
		}
		return "", 0, errors.New("Cobalt media request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.ContentLength > budget {
		return "", 0, errors.New("Cobalt media unavailable or too large")
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(resp.Body, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", 0, errors.New("Cobalt media read failed")
	}
	if n == 0 {
		return "", 0, errors.New("Cobalt returned empty media")
	}
	head = head[:n]
	detected := http.DetectContentType(head)
	declared, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if detected == "text/html; charset=utf-8" || strings.HasPrefix(detected, "text/") || declared == "application/json" {
		return "", 0, errors.New("Cobalt returned non-media content")
	}
	ext := ".mp4"
	switch detected {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}
	if item.Type == "photo" && MediaType("x"+ext, "") != "photo" {
		return "", 0, errors.New("Cobalt returned an invalid image")
	}
	if r.Mode == "audio" {
		ext = strings.ToLower(filepath.Ext(item.Filename))
		switch ext {
		case ".mp3", ".m4a", ".ogg", ".opus", ".wav", ".webm", ".flac":
		default:
			return "", 0, errors.New("invalid Cobalt audio format")
		}
	}
	path := filepath.Join(dir, fmt.Sprintf("%03d%s", index+1, ext))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", 0, err
	}
	written, copyErr := io.Copy(file, io.LimitReader(io.MultiReader(bytes.NewReader(head), resp.Body), budget+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written > budget || (resp.ContentLength >= 0 && written != resp.ContentLength) {
		os.Remove(path)
		return "", 0, errors.New("Cobalt download failed or exceeded size limit")
	}
	return path, written, nil
}
