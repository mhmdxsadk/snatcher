package download

import (
	"strings"

	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestCobaltGallery(t *testing.T) {
	var photo bytes.Buffer
	jpeg.Encode(&photo, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			if r.Method != "GET" || r.Header.Get("Authorization") != "" {
				t.Error("media request method or leaked credentials")
			}
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(photo.Bytes())
			return
		}
		if r.Header.Get("Authorization") != "Api-Key test" {
			t.Error("missing API key")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["localProcessing"] != "disabled" || body["alwaysProxy"] != true {
			t.Error(body)
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "picker", "picker": []cobaltItem{{Type: "photo", URL: server.URL + "/one"}, {Type: "photo", URL: server.URL + "/two"}}})
	}))
	defer server.Close()
	c := Cobalt{Endpoint: server.URL, APIKey: "test"}
	files, err := c.Run(context.Background(), Request{Mode: "auto", Quality: "1080", AudioFormat: "mp3"}, t.TempDir())
	if err != nil || len(files) != 2 {
		t.Fatalf("%v %v", files, err)
	}
	for _, file := range files {
		data, _ := os.ReadFile(file)
		if !bytes.Equal(data, photo.Bytes()) {
			t.Fatal("incorrect downloaded bytes")
		}
	}
}
func TestCobaltRejectResponses(t *testing.T) {
	for _, body := range []string{`{"status":"error"}`, `{"status":"picker","picker":[]}`, `{"status":"local-processing"}`, `{"status":"tunnel","url":"file:///etc/passwd"}`, `not json`} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer s.Close()
			c := Cobalt{Endpoint: s.URL}
			if _, err := c.Run(context.Background(), Request{}, t.TempDir()); err == nil {
				t.Fatal("accepted invalid response")
			}
		})
	}
}
func TestCobaltMediaValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body, kind string
		budget           int64
	}{{"empty", "", "", 100}, {"html", "<html>error</html>", "", 100}, {"oversize", "\xff\xd8\xff\xe0xxxxxxxx", "photo", 2}, {"invalid-photo", "\x00\x01\x02\x03", "photo", 100}} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tc.body)) }))
			defer s.Close()
			if _, _, err := fetchCobaltMedia(context.Background(), cobaltItem{URL: s.URL, Type: tc.kind}, Request{}, t.TempDir(), 0, tc.budget); err == nil {
				t.Fatal("accepted invalid media")
			}
		})
	}
}
func TestCobaltNoAPIRedirect(t *testing.T) {
	called := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer dest.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dest.URL, 307) }))
	defer source.Close()
	c := Cobalt{Endpoint: source.URL, APIKey: "secret"}
	_, err := c.Run(context.Background(), Request{}, t.TempDir())
	if err == nil || called {
		t.Fatal("followed API redirect")
	}
}

func TestCobaltRejectsTruncatedImageAndFakeAudio(t *testing.T) {
	for _, mode := range []string{"auto", "audio"} {
		t.Run(mode, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/" {
					json.NewEncoder(w).Encode(map[string]any{"status": "tunnel", "url": server.URL + "/media", "filename": "audio.mp3"})
					return
				}
				w.Write([]byte{255, 216, 255})
			}))
			defer server.Close()
			_, err := (&Cobalt{Endpoint: server.URL}).Run(context.Background(), Request{Mode: mode}, t.TempDir())
			if err == nil {
				t.Fatal("accepted invalid media")
			}
		})
	}
}

func TestCobaltErrorCodeDiagnostic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"status":"error","error":{"code":"error.api.fetch.login"}}`))
	}))
	defer server.Close()
	_, err := (&Cobalt{Endpoint: server.URL}).Run(context.Background(), Request{}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "error.api.fetch.login") {
		t.Fatalf("%v", err)
	}
}

func TestCobaltRejectsIncompleteHTTPBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "200")
		w.Write([]byte{255, 216, 255})
	}))
	defer server.Close()
	_, _, err := fetchCobaltMedia(context.Background(), cobaltItem{URL: server.URL, Type: "photo"}, Request{}, t.TempDir(), 0, 1000)
	if err == nil {
		t.Fatal("accepted incomplete HTTP body")
	}
}
