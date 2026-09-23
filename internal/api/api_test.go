package api

import (
	"encoding/json"
	"github.com/mhmdxsadk/snatcher/internal/security"
	"github.com/mhmdxsadk/snatcher/internal/version"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestRequestValidation(t *testing.T) {
	tests := []struct {
		name, method, path, contentType, body string
		status                                int
		code, allow                           string
	}{
		{"method", "GET", "/v1/snatch", "", "", 405, "method_not_allowed", "POST"},
		{"health method", "POST", "/health", "", "", 405, "method_not_allowed", "GET, HEAD"},
		{"root method", "POST", "/", "", "", 405, "method_not_allowed", "GET, HEAD"},
		{"renamed route", "POST", "/v1/snatcher", "application/json", `{}`, 404, "not_found", ""},
		{"trailing slash", "POST", "/v1/snatch/", "application/json", `{}`, 404, "not_found", ""},
		{"old route", "POST", "/download", "application/json", `{}`, 404, "not_found", ""},
		{"unversioned route", "POST", "/snatch", "application/json", `{}`, 404, "not_found", ""},
		{"unsupported version", "POST", "/v2/snatch", "application/json", `{}`, 404, "not_found", ""},
		{"unknown route", "GET", "/missing", "", "", 404, "not_found", ""},
		{"content type", "POST", "/v1/snatch", "text/plain", `{}`, 415, "invalid_content_type", ""},
		{"null", "POST", "/v1/snatch", "application/json", `null`, 400, "invalid_request", ""},
		{"multiple values", "POST", "/v1/snatch", "application/json", `{} {}`, 400, "invalid_request", ""},
		{"unknown field", "POST", "/v1/snatch", "application/json", `{"url":"https://example.com","extra":true}`, 400, "invalid_request", ""},
		{"missing URL", "POST", "/v1/snatch", "application/json", `{}`, 400, "invalid_url", ""},
		{"credentials", "POST", "/v1/snatch", "application/json", `{"url":"https://user:password@example.com"}`, 400, "invalid_url", ""},
		{"oversized", "POST", "/v1/snatch", "application/json", `{"url":"https://example.com/` + strings.Repeat("x", 16<<10) + `"}`, 413, "request_too_large", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			r.Header.Set("Content-Type", tt.contentType)
			w := httptest.NewRecorder()
			// Invalid requests must return before submitting a job.
			NewHandler(nil, "").ServeHTTP(w, r)
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
	NewHandler(nil, "").ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
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
			r := httptest.NewRequest("POST", "/v1/snatch", strings.NewReader(`{"url":"https://example.com/video"`+tt.fields+`}`))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			NewHandler(nil, "").ServeHTTP(w, r)
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

func TestServiceInfo(t *testing.T) {
	guard := security.New(security.Config{
		APIKey: strings.Repeat("k", 32), IPPerMinute: 20, IPBurst: 5,
		GlobalPerMinute: 60, GlobalBurst: 10, MaxConcurrent: 2, MaxClients: 100,
	})
	server := httptest.NewServer(guard.Wrap(NewHandler(nil, "")))
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
		want := map[string]string{"name": "Snatcher", "version": version.Release, "api": version.API}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("metadata = %v, want %v", got, want)
		}
	}
	resp, err := server.Client().Post(server.URL+"/v1/snatch", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated resolution returned %d", resp.StatusCode)
	}
}
