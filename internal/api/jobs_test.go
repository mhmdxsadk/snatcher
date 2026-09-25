package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhmdxsadk/snatcher/internal/download"
)

type testDownloadRunner struct{}

func (testDownloadRunner) Run(_ context.Context, _ download.Request, d string) ([]string, error) {
	p := filepath.Join(d, "clip.mp4")
	return []string{p}, os.WriteFile(p, []byte("test media bytes"), 0600)
}
func TestDownloadJobLifecycle(t *testing.T) {
	m, err := download.New(t.TempDir(), testDownloadRunner{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	h := NewHandler(m, "test secret")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/snatch", strings.NewReader(`{"url":"https://example.com/video"}`))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	location := w.Header().Get("Location")
	var result struct {
		Status string
		Items  []Item
	}
	for i := 0; i < 100; i++ {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", location, nil))
		json.Unmarshal(w.Body.Bytes(), &result)
		if result.Status == "completed" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(result.Items) != 1 {
		t.Fatalf("%s", w.Body)
	}
	link := result.Items[0].URL
	if !strings.HasPrefix(link, "http://example.com/download/") {
		t.Fatalf("expected absolute download URL: %s", link)
	}
	for _, origin := range []string{"https://snatcher.example", "http://localhost:8080", "http://[::1]:8080"} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest("GET", origin+location, nil))
		var got struct{ Items []Item }
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Items) != 1 || !strings.HasPrefix(got.Items[0].URL, origin+"/download/") {
			t.Fatalf("incorrect origin: %s", response.Body)
		}
	}
	for _, method := range []string{"GET", "HEAD"} {
		w = httptest.NewRecorder()
		r = httptest.NewRequest(method, link, nil)
		r.Header.Set("Range", "bytes=0-3")
		h.ServeHTTP(w, r)
		if w.Code != http.StatusPartialContent {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body)
		}
		if method == "GET" && w.Body.String() != "test" {
			t.Fatal(w.Body)
		}
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD body")
		}
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", link+"bad", nil))
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

type galleryRunner struct{}

func (galleryRunner) Run(_ context.Context, _ download.Request, dir string) ([]string, error) {
	paths := []string{filepath.Join(dir, "001.jpg"), filepath.Join(dir, "002.mp4")}
	for i, path := range paths {
		if err := os.WriteFile(path, []byte(strings.Repeat(string(rune('a'+i)), 8)), 0600); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func TestGalleryLinks(t *testing.T) {
	manager, err := download.New(t.TempDir(), galleryRunner{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	job, err := manager.Submit(download.Request{Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(manager, "test secret")
	var result struct{ Items []Item }
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/v1/jobs/"+job.ID, nil))
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Items) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(result.Items) != 2 || result.Items[0].Type != "photo" || result.Items[1].Type != "video" {
		t.Fatalf("%+v", result)
	}
	for i, item := range result.Items {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", item.URL, nil))
		if w.Code != 200 || w.Body.String() != strings.Repeat(string(rune('a'+i)), 8) {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	// A signature for one item must not authorize another item.
	tampered := strings.Replace(result.Items[1].URL, "/1?", "?", 1)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", tampered, nil))
	if w.Code != 403 {
		t.Fatalf("item substitution accepted: %d", w.Code)
	}
}
