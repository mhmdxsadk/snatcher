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

func (testDownloadRunner) Run(_ context.Context, _ download.Request, d string) (string, error) {
	p := filepath.Join(d, "clip.mp4")
	return p, os.WriteFile(p, []byte("test media bytes"), 0600)
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
