// Package client implements the upstream protocol, not Snatcher's public API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mhmdxsadk/snatcher/internal/config"
)

const maxResponseBytes = 1 << 20

type Client struct {
	endpoint string
	http     *http.Client
}

func New(baseURL string) (*Client, error) {
	if err := config.ValidateCobaltURL(baseURL); err != nil {
		return nil, err
	}
	return &Client{
		endpoint: strings.TrimRight(baseURL, "/") + "/",
		http: &http.Client{
			Timeout: 30 * time.Second,
			// A protocol "redirect" is JSON, not an HTTP redirect.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

type Response struct {
	Status        string       `json:"status"`
	URL           string       `json:"url"`
	Filename      string       `json:"filename"`
	Picker        []PickerItem `json:"picker"`
	AudioFilename string       `json:"audioFilename"`
	// Audio is a URL for pickers and an object for local processing.
	Audio  json.RawMessage `json:"audio"`
	Type   string          `json:"type"`
	Tunnel []string        `json:"tunnel"`
	Output json.RawMessage `json:"output"`
	Error  *APIError       `json:"error"`
}

type PickerItem struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type APIError struct {
	Code       string `json:"code"`
	HTTPStatus int    `json:"-"`
}

func (e *APIError) Error() string { return fmt.Sprintf("cobalt: %s (HTTP %d)", e.Code, e.HTTPStatus) }

type HTTPError struct{ StatusCode int }

func (e *HTTPError) Error() string {
	return fmt.Sprintf("cobalt: unexpected HTTP status %d", e.StatusCode)
}

// Options contains validated download preferences supplied by the API.
type Options struct {
	Quality     string
	Mode        string
	AudioFormat string
}

// Resolve asks Cobalt for media instructions. It does not download or process media.
// Callers must handle local-processing before treating any result as downloadable.
func (c *Client) Resolve(ctx context.Context, sourceURL string, options Options) (*Response, error) {
	body, err := json.Marshal(struct {
		URL                   string `json:"url"`
		AlwaysProxy           bool   `json:"alwaysProxy"`
		LocalProcessing       string `json:"localProcessing"`
		VideoQuality          string `json:"videoQuality"`
		DownloadMode          string `json:"downloadMode"`
		AudioFormat           string `json:"audioFormat"`
		AudioBitrate          string `json:"audioBitrate"`
		YouTubeVideoCodec     string `json:"youtubeVideoCodec"`
		YouTubeVideoContainer string `json:"youtubeVideoContainer"`
		AllowH265             bool   `json:"allowH265"`
	}{
		URL: sourceURL, AlwaysProxy: true, LocalProcessing: "disabled",
		VideoQuality: options.Quality, DownloadMode: options.Mode,
		AudioFormat: options.AudioFormat, AudioBitrate: "128",
		YouTubeVideoCodec: "h264", YouTubeVideoContainer: "mp4",
		// Allow existing TikTok HEVC formats; this does not transcode media.
		AllowH265: true,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cobalt request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cobalt request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read cobalt response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("cobalt response exceeds %d bytes", maxResponseBytes)
	}
	var result Response
	decodeErr := json.Unmarshal(data, &result)
	if decodeErr == nil && result.Status == "error" && result.Error != nil && result.Error.Code != "" {
		result.Error.HTTPStatus = resp.StatusCode
		return nil, result.Error
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{StatusCode: resp.StatusCode}
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("decode cobalt response: %w", decodeErr)
	}
	switch result.Status {
	case "tunnel", "redirect":
		if result.URL == "" {
			return nil, fmt.Errorf("cobalt media response missing URL")
		}
	case "picker":
		if len(result.Picker) == 0 {
			return nil, fmt.Errorf("cobalt picker response has no items")
		}
		for _, item := range result.Picker {
			if item.URL == "" {
				return nil, fmt.Errorf("cobalt picker item missing URL")
			}
		}
	case "local-processing":
		if result.Type == "" || len(result.Tunnel) == 0 || len(result.Output) == 0 || string(result.Output) == "null" {
			return nil, fmt.Errorf("cobalt local-processing response missing instructions")
		}
	default:
		return nil, fmt.Errorf("cobalt response has an invalid or unsupported status")
	}
	return &result, nil
}
