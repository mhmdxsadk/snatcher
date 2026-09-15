package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/mhmdxsadk/snatcher/internal/client"
)

func TestDownloadIntegration(t *testing.T) {
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
				want := map[string]any{"url": "https://www.instagram.com/p/example/?img_index=2", "alwaysProxy": true, "localProcessing": "disabled"}
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
			req := httptest.NewRequest("POST", "/download", strings.NewReader(`{"url":" https://www.instagram.com/p/example/?igsh=tracking&img_index=2 "}`))
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
		{"method", "GET", "/download", "", "", 405, "method_not_allowed", "POST"},
		{"health method", "POST", "/health", "", "", 405, "method_not_allowed", "GET, HEAD"},
		{"unknown route", "GET", "/missing", "", "", 404, "not_found", ""},
		{"content type", "POST", "/download", "text/plain", `{}`, 415, "invalid_content_type", ""},
		{"null", "POST", "/download", "application/json", `null`, 400, "invalid_request", ""},
		{"multiple values", "POST", "/download", "application/json", `{} {}`, 400, "invalid_request", ""},
		{"unknown field", "POST", "/download", "application/json", `{"url":"https://example.com","extra":true}`, 400, "invalid_request", ""},
		{"missing URL", "POST", "/download", "application/json", `{}`, 400, "invalid_url", ""},
		{"credentials", "POST", "/download", "application/json", `{"url":"https://user:password@example.com"}`, 400, "invalid_url", ""},
		{"oversized", "POST", "/download", "application/json", `{"url":"https://example.com/` + strings.Repeat("x", 16<<10) + `"}`, 413, "request_too_large", ""},
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
