package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/mhmdxsadk/snatcher/internal/security"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
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
	r := httptest.NewRequest("POST", "/v2/snatch", strings.NewReader(`{"url":"https://example.com/video"}`))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	location := w.Header().Get("Location")
	var submitted struct{ Token string }
	json.Unmarshal(w.Body.Bytes(), &submitted)
	if len(submitted.Token) != 64 {
		t.Fatal("missing job token")
	}
	var result struct {
		Status string
		Items  []Item
	}
	for i := 0; i < 100; i++ {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, jobRequest("GET", location, submitted.Token))
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
		h.ServeHTTP(response, jobRequest("GET", origin+location, submitted.Token))
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
		handler.ServeHTTP(w, jobRequest("GET", "/v2/jobs/"+job.ID, job.Token))
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

func TestDownloadOriginBehindTrustedProxy(t *testing.T) {
	m, err := download.New(t.TempDir(), testDownloadRunner{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	j, _ := m.Submit(download.Request{})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		v, _ := m.Get(j.ID)
		if v.Status == download.StatusCompleted {
			break
		}
		time.Sleep(time.Millisecond)
	}
	key := strings.Repeat("k", 32)
	cfg := security.Config{APIKey: key, IPPerMinute: 20, IPBurst: 5, GlobalPerMinute: 60, GlobalBurst: 10, MaxConcurrent: 4, MaxClients: 100, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.30.0.1/32")}}
	h := security.New(cfg).Wrap(NewHandler(m, key))
	for _, peer := range []string{"172.30.0.1:1234", "192.0.2.1:1234"} {
		r := httptest.NewRequest("GET", "http://snatcher.example/v2/jobs/"+j.ID, nil)
		r.RemoteAddr = peer
		r.Header.Set("X-API-Key", key)
		r.Header.Set("X-Job-Token", j.Token)
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var result struct{ Items []Item }
		json.Unmarshal(w.Body.Bytes(), &result)
		scheme := "http://"
		if strings.HasPrefix(peer, "172.30.") {
			scheme = "https://"
		}
		if w.Code != 200 || len(result.Items) != 1 || !strings.HasPrefix(result.Items[0].URL, scheme+"snatcher.example/download/") {
			t.Fatalf("%s: %s", peer, w.Body)
		}
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline time.Time
	entered  chan struct{}
	release  chan struct{}
}

func (w *deadlineRecorder) SetWriteDeadline(d time.Time) error {
	w.deadline = d
	return nil
}
func (w *deadlineRecorder) Write(p []byte) (int, error) {
	if w.entered != nil {
		w.entered <- struct{}{}
		<-w.release
	}
	return w.ResponseRecorder.Write(p)
}

func TestTransferDeadlines(t *testing.T) {
	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	writer := &transferWriter{ResponseWriter: recorder, deadline: time.Now().Add(time.Minute)}
	before := time.Now()
	if _, err := writer.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if recorder.deadline.Before(before.Add(30*time.Second)) || recorder.deadline.After(time.Now().Add(30*time.Second)) {
		t.Fatal("idle deadline not applied")
	}
	writer.deadline = time.Now().Add(time.Second)
	writer.Write([]byte("second"))
	if !recorder.deadline.Equal(writer.deadline) {
		t.Fatal("write deadline exceeds total/expiry deadline")
	}
	writer.deadline = time.Now().Add(-time.Second)
	if _, err := writer.Write([]byte("expired")); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expired transfer continued: %v", err)
	}
}

func TestTransferCapacity(t *testing.T) {
	m, err := download.New(t.TempDir(), testDownloadRunner{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	job, err := m.Submit(download.Request{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		job, _ = m.Get(job.ID)
		if job.Status == download.StatusCompleted {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if job.Status != download.StatusCompleted {
		t.Fatal(job)
	}
	expiry := strconv.FormatInt(job.Expires.Unix(), 10)
	link := "/download/" + job.ID + "?exp=" + expiry + "&sig=" + downloadSignature(job.Token, job.ID, expiry)
	h := NewHandler(m, "key")
	entered, release, done := make(chan struct{}, 4), make(chan struct{}), make(chan struct{}, 4)
	defer close(release)
	for i := 0; i < 4; i++ {
		go func() {
			w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), entered: entered, release: release}
			h.ServeHTTP(w, httptest.NewRequest("GET", link, nil))
			done <- struct{}{}
		}()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("transfer did not start")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", link, nil))
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("unbounded transfers: %d", w.Code)
	}
	// Release one slot and verify another request can complete.
	release <- struct{}{}
	<-done
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", link, nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func jobRequest(method, path, token string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("X-Job-Token", token)
	return r
}

func TestJobTokenIsolation(t *testing.T) {
	m, err := download.New(t.TempDir(), testDownloadRunner{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	a, _ := m.Submit(download.Request{})
	b, _ := m.Submit(download.Request{})
	if a.Token == b.Token {
		t.Fatal("job tokens reused")
	}
	h := NewHandler(m, "shared-key")
	for _, method := range []string{"GET", "POST", "DELETE"} {
		path := "/v2/jobs/" + a.ID
		if method == "POST" {
			path += "/cancel"
		}
		for _, token := range []string{"", b.Token, "shared-key"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, jobRequest(method, path, token))
			if w.Code != 404 {
				t.Fatalf("%s accepted unrelated token: %d", method, w.Code)
			}
		}
	}
	for i := 0; i < 1000; i++ {
		a, _ = m.Get(a.ID)
		if a.Status == download.StatusCompleted {
			break
		}
		time.Sleep(time.Millisecond)
	}
	expiry := strconv.FormatInt(a.Expires.Unix(), 10)
	w := httptest.NewRecorder()
	forged := "/download/" + a.ID + "?exp=" + expiry + "&sig=" + downloadSignature("shared-key", a.ID, expiry)
	h.ServeHTTP(w, httptest.NewRequest("GET", forged, nil))
	if w.Code != 403 {
		t.Fatal("shared key forged download", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, jobRequest("GET", "/v2/jobs/"+a.ID, a.Token))
	if w.Code != 200 || strings.Contains(w.Body.String(), a.Token) {
		t.Fatal("token leaked or polling failed")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, jobRequest("DELETE", "/v2/jobs/"+a.ID, a.Token))
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, ok := m.Get(a.ID); ok {
		t.Fatal("job survived deletion")
	}
	if _, err := os.Stat(a.Files[0]); !os.IsNotExist(err) {
		t.Fatal("file survived deletion")
	}
}
