package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mhmdxsadk/snatcher/internal/client"
	"github.com/mhmdxsadk/snatcher/internal/security"
)

func TestSnatcherIntegration(t *testing.T) {
	tests := []struct {
		name, response             string
		upstreamStatus, wantStatus int
		wantItems                  []Item
		wantCode                   string
	}{
		{"tunnel", `{"status":"tunnel","url":"https://media.example/tunnel?id=1","filename":"../clip.mp4"}`, 200, 200, []Item{{"https://media.example/tunnel?id=1", "clip.mp4", "video"}}, ""},
		{"redirect", `{"status":"redirect","url":"https://media.example/photo.jpg"}`, 200, 200, []Item{{"https://media.example/photo.jpg", "", "photo"}}, ""},
		{"picker", `{"status":"picker","picker":[{"type":"photo","url":"https://media.example/1"},{"type":"video","url":"https://media.example/2"}],"audio":"https://media.example/audio"}`, 200, 200, []Item{{"https://media.example/1", "", "photo"}, {"https://media.example/2", "", "video"}}, ""},
		{"local processing", `{"status":"local-processing","type":"merge","tunnel":["https://media.example/1"],"output":{"filename":"clip.mp4"}}`, 200, 422, nil, "processing_required"},
		{"unavailable", `{"status":"error","error":{"code":"error.api.fetch.empty"}}`, 400, 422, nil, "media_unavailable"},
		{"rate limited", `{"status":"error","error":{"code":"error.api.rate_limit"}}`, 429, 503, nil, "upstream_busy"},
		{"non JSON rate limit", `busy`, 429, 503, nil, "upstream_busy"},
		{"malformed upstream", `<html>bad gateway</html>`, 502, 502, nil, "upstream_error"},
		{"empty picker", `{"status":"picker","picker":[]}`, 200, 502, nil, "upstream_error"},
		{"unsafe media URL", `{"status":"tunnel","url":"file:///etc/passwd","filename":"clip.mp4"}`, 200, 502, nil, "invalid_upstream_response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/cobalt/" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
					t.Error("missing JSON headers")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				want := map[string]any{"url": "https://www.instagram.com/p/example/?img_index=2", "alwaysProxy": true, "localProcessing": "disabled", "videoQuality": "1080", "downloadMode": "auto", "audioFormat": "mp3", "audioBitrate": "128", "youtubeVideoCodec": "h264", "youtubeVideoContainer": "mp4", "allowH265": true}
				if !reflect.DeepEqual(body, want) {
					t.Errorf("upstream body = %#v", body)
				}
				w.WriteHeader(tt.upstreamStatus)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer upstream.Close()
			c, err := client.New(upstream.URL + "/cobalt")
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/v1/snatcher", strings.NewReader(`{"url":" https://www.instagram.com/p/example/?igsh=tracking&img_index=2 "}`))
			req.Header.Set("Content-Type", "application/json; charset=utf-8")
			w := httptest.NewRecorder()
			NewHandler(c).ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body)
			}
			var body struct {
				Status string
				Items  []Item
				Error  struct{ Code string }
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(body.Items, tt.wantItems) || body.Error.Code != tt.wantCode {
				t.Errorf("unexpected response: %s", w.Body)
			}
			if (tt.wantStatus == 200 && body.Status != "success") || (tt.wantStatus != 200 && body.Status != "error") {
				t.Errorf("unexpected envelope: %s", w.Body)
			}
		})
	}
}

func TestRequestValidation(t *testing.T) {
	tests := []struct {
		name, method, path, contentType, body string
		status                                int
		code, allow                           string
	}{
		{"method", "GET", "/v1/snatcher", "", "", 405, "method_not_allowed", "POST"},
		{"health method", "POST", "/health", "", "", 405, "method_not_allowed", "GET, HEAD"},
		{"root method", "POST", "/", "", "", 405, "method_not_allowed", "GET, HEAD"},
		{"old route", "POST", "/download", "application/json", `{}`, 404, "not_found", ""},
		{"unversioned route", "POST", "/snatcher", "application/json", `{}`, 404, "not_found", ""},
		{"unsupported version", "POST", "/v2/snatcher", "application/json", `{}`, 404, "not_found", ""},
		{"unknown route", "GET", "/missing", "", "", 404, "not_found", ""},
		{"content type", "POST", "/v1/snatcher", "text/plain", `{}`, 415, "invalid_content_type", ""},
		{"null", "POST", "/v1/snatcher", "application/json", `null`, 400, "invalid_request", ""},
		{"multiple values", "POST", "/v1/snatcher", "application/json", `{} {}`, 400, "invalid_request", ""},
		{"unknown field", "POST", "/v1/snatcher", "application/json", `{"url":"https://example.com","extra":true}`, 400, "invalid_request", ""},
		{"missing URL", "POST", "/v1/snatcher", "application/json", `{}`, 400, "invalid_url", ""},
		{"credentials", "POST", "/v1/snatcher", "application/json", `{"url":"https://user:password@example.com"}`, 400, "invalid_url", ""},
		{"oversized", "POST", "/v1/snatcher", "application/json", `{"url":"https://example.com/` + strings.Repeat("x", 16<<10) + `"}`, 413, "request_too_large", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			r.Header.Set("Content-Type", tt.contentType)
			w := httptest.NewRecorder()
			// Invalid requests must return before accessing Cobalt.
			NewHandler(nil).ServeHTTP(w, r)
			var body struct{ Error struct{ Code string } }
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != tt.status || body.Error.Code != tt.code || w.Header().Get("Allow") != tt.allow {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body)
			}
			if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
				t.Error("missing response headers")
			}
		})
	}
}

func TestHealth(t *testing.T) {
	w := httptest.NewRecorder()
	NewHandler(nil).ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("unexpected health: %d %s", w.Code, w.Body)
	}
}

func TestNormalizeURL(t *testing.T) {
	for _, tt := range []struct{ raw, want string }{
		{" https://youtu.be/abc?t=20 ", "https://youtu.be/abc?t=20"},
		{"https://www.youtube.com/watch?v=abc&list=xyz", "https://www.youtube.com/watch?v=abc&list=xyz"},
		{"https://vm.tiktok.com/abc/?_t=tracking&_r=1", "https://vm.tiktok.com/abc/"},
		{"https://instagram.com/p/abc/?igsh=tracking&img_index=2", "https://instagram.com/p/abc/?img_index=2"},
		{"https://notinstagram.com/p/abc/?igsh=keep", "https://notinstagram.com/p/abc/?igsh=keep"},
	} {
		got, err := normalizeURL(tt.raw)
		if err != nil || got != tt.want {
			t.Errorf("normalizeURL(%q) = %q, %v", tt.raw, got, err)
		}
	}
}

func TestUpstreamTimeout(t *testing.T) {
	w := httptest.NewRecorder()
	upstreamError(w, context.DeadlineExceeded)
	if w.Code != 504 || !strings.Contains(w.Body.String(), `"code":"upstream_timeout"`) {
		t.Fatalf("unexpected timeout: %d %s", w.Code, w.Body)
	}
}

func TestDownloadOptions(t *testing.T) {
	tests := []struct {
		name, fields, quality, mode, format, filename, kind string
	}{
		{"720p", `,"quality":"720"`, "720", "auto", "mp3", "video.mp4", "video"},
		{"1080p", `,"quality":"1080"`, "1080", "auto", "mp3", "video.mp4", "video"},
		{"1440p", `,"quality":"1440"`, "1440", "auto", "mp3", "video.mp4", "video"},
		{"4K silent", `,"quality":"2160","mode":"mute"`, "2160", "mute", "mp3", "video.mp4", "video"},
		{"maximum", `,"quality":"max"`, "max", "auto", "mp3", "video.mp4", "video"},
		{"MP3", `,"mode":"audio"`, "1080", "audio", "mp3", "audio.mp3", "audio"},
		{"best audio", `,"mode":"audio","audioFormat":"best"`, "1080", "audio", "best", "audio.opus", "audio"},
		{"empty defaults", `,"quality":"","mode":"","audioFormat":""`, "1080", "auto", "mp3", "video.mp4", "video"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				want := map[string]any{
					"url": "https://www.youtube.com/watch?v=example", "alwaysProxy": true,
					"localProcessing": "disabled", "videoQuality": tt.quality, "downloadMode": tt.mode,
					"audioFormat": tt.format, "audioBitrate": "128", "youtubeVideoCodec": "h264", "youtubeVideoContainer": "mp4", "allowH265": true,
				}
				if !reflect.DeepEqual(body, want) {
					t.Errorf("Cobalt request = %#v, want %#v", body, want)
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "tunnel", "url": "https://media.example/file", "filename": tt.filename})
			}))
			defer upstream.Close()
			c, err := client.New(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/v1/snatcher", strings.NewReader(`{"url":"https://www.youtube.com/watch?v=example"`+tt.fields+`}`))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			NewHandler(c).ServeHTTP(w, r)
			var body struct{ Items []Item }
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || len(body.Items) != 1 || body.Items[0].Type != tt.kind {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestInvalidOptions(t *testing.T) {
	for _, tt := range []struct{ fields, code string }{
		{`,"quality":"4k"`, "invalid_options"},
		{`,"quality":1080`, "invalid_request"},
		{`,"mode":"video"`, "invalid_options"},
		{`,"mode":true`, "invalid_request"},
		{`,"audioFormat":"wav"`, "invalid_options"},
		{`,"audioFormat":[]`, "invalid_request"},
		{`,"mode":"audio","quality":"typo"`, "invalid_options"},
		{`,"mode":"mute","audioFormat":"typo"`, "invalid_options"},
		{`,"youtubeVideoCodec":"av1"`, "invalid_request"},
	} {
		t.Run(tt.fields, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/snatcher", strings.NewReader(`{"url":"https://example.com/video"`+tt.fields+`}`))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			NewHandler(nil).ServeHTTP(w, r)
			var body struct{ Error struct{ Code string } }
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != 400 || body.Error.Code != tt.code {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestAudioGallery(t *testing.T) {
	for _, tt := range []struct {
		name, audio string
		status      int
		code        string
	}{
		{"background audio", `,"audio":"https://media.example/sound","audioFilename":"sound.m4a"`, 200, ""},
		{"no audio", "", 422, "audio_unavailable"},
		{"null audio", `,"audio":null`, 422, "audio_unavailable"},
		{"empty audio", `,"audio":""`, 422, "audio_unavailable"},
		{"malformed audio", `,"audio":{}`, 502, "invalid_upstream_response"},
		{"invalid audio URL", `,"audio":"file:///audio"`, 502, "invalid_upstream_response"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"status":"picker","picker":[{"type":"photo","url":"https://media.example/photo"}]` + tt.audio + `}`))
			}))
			defer upstream.Close()
			c, err := client.New(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/v1/snatcher", strings.NewReader(`{"url":"https://example.com/gallery","mode":"audio"}`))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			NewHandler(c).ServeHTTP(w, r)
			var body struct {
				Items []Item
				Error struct{ Code string }
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != tt.status || body.Error.Code != tt.code {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body)
			}
			if tt.status == 200 && !reflect.DeepEqual(body.Items, []Item{{URL: "https://media.example/sound", Filename: "sound.m4a", Type: "audio"}}) {
				t.Fatalf("unexpected audio: %s", w.Body)
			}
		})
	}
}

func TestTunnelProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tunnel" || r.URL.Query().Get("sig") != "a+b" {
			t.Errorf("unexpected upstream URL: %s", r.URL)
		}
		if r.Header.Get("Range") != "bytes=0-3" {
			t.Errorf("missing range: %v", r.Header)
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 0-3/10")
		w.Header().Set("Set-Cookie", "private=value")
		w.WriteHeader(http.StatusPartialContent)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("data"))
		}
	}))
	defer upstream.Close()
	c, err := client.New(upstream.URL + "/api/")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			r := httptest.NewRequest(method, "/tunnel?id=1&exp=2&sig=a%2Bb", nil)
			r.Header.Set("Range", "bytes=0-3")
			w := httptest.NewRecorder()
			NewHandler(c).ServeHTTP(w, r)
			if w.Code != http.StatusPartialContent || w.Header().Get("Content-Range") != "bytes 0-3/10" || w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("unexpected response: %d %v", w.Code, w.Header())
			}
			want := "data"
			if method == http.MethodHead {
				want = ""
			}
			if w.Body.String() != want {
				t.Fatalf("body = %q, want %q", w.Body.String(), want)
			}
		})
	}
}

func TestRewriteTunnelURL(t *testing.T) {
	c, err := client.New("http://media-service:9000/api/")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ raw, proto, want string }{
		{"http://media-service:9000/api/tunnel?id=1&sig=a%2Bb", "", "http://snatcher.example/tunnel?id=1&sig=a%2Bb"},
		{"http://media-service:9000/api/tunnel?id=1", "https", "https://snatcher.example/tunnel?id=1"},
		{"https://external.example/tunnel?id=1", "", "https://external.example/tunnel?id=1"},
		{"http://media-service:9001/api/tunnel?id=1", "", "http://media-service:9001/api/tunnel?id=1"},
	} {
		r := httptest.NewRequest(http.MethodPost, "http://snatcher.example/v1/snatcher", nil)
		r.Header.Set("X-Forwarded-Proto", tt.proto)
		got, err := rewriteTunnelURL(c, r, tt.raw)
		if err != nil || got != tt.want {
			t.Errorf("rewrite %q = %q, %v; want %q", tt.raw, got, err, tt.want)
		}
	}
}

func TestInvalidTunnelRequest(t *testing.T) {
	for _, target := range []string{"/tunnel", "/tunnel?id=1&exp=2", "/tunnel?id=1&exp=2&sig=x&bad=%zz"} {
		w := httptest.NewRecorder()
		NewHandler(nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d", target, w.Code)
		}
	}
}

func TestTruncatedTunnelAbortsResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("partial"))
	}))
	defer upstream.Close()
	c, err := client.New(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if got := recover(); got != http.ErrAbortHandler {
			t.Errorf("panic = %v, want http.ErrAbortHandler", got)
		}
	}()
	NewHandler(c).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/tunnel?id=1&exp=2&sig=x", nil))
}

func TestTunnelOutlastsServerWriteTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte("media"))
	}))
	defer upstream.Close()
	c, err := client.New(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(NewHandler(c))
	server.Config.WriteTimeout = 20 * time.Millisecond
	server.Start()
	defer server.Close()
	httpClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := httpClient.Get(server.URL + "/tunnel?id=1&exp=2&sig=x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "media" || resp.StatusCode != http.StatusOK {
		t.Fatalf("response = %d %q, %v", resp.StatusCode, body, err)
	}
}

func TestServiceInfo(t *testing.T) {
	guard := security.New(security.Config{
		APIKey: strings.Repeat("k", 32), IPPerMinute: 20, IPBurst: 5,
		GlobalPerMinute: 60, GlobalBurst: 10, MaxConcurrent: 2, MaxClients: 100,
	})
	server := httptest.NewServer(guard.Wrap(NewHandler(nil)))
	defer server.Close()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r, err := http.NewRequest(method, server.URL+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("%s: status = %d, body = %q, error = %v", method, resp.StatusCode, body, err)
		}
		if method == http.MethodHead {
			if len(body) != 0 {
				t.Errorf("HEAD returned a body: %q", body)
			}
			continue
		}
		var got map[string]string
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"name": "Snatcher", "version": "0.1.0", "api": "v1"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("metadata = %v, want %v", got, want)
		}
	}
	resp, err := server.Client().Post(server.URL+"/v1/snatcher", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated resolution returned %d", resp.StatusCode)
	}
}

func TestTunnelPreservesContentEncoding(t *testing.T) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write([]byte("media")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer upstream.Close()
	c, err := client.New(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	NewHandler(c).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tunnel?id=1&exp=2&sig=x", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(w.Body.Bytes(), compressed.Bytes()) {
		t.Fatalf("encoded response was altered: status=%d headers=%v", w.Code, w.Header())
	}
}
