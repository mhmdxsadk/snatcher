package download

import (
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

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

func TestCobaltMediaFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		length string
		want   string
	}{
		{"forbidden", 403, "0", "item 2 returned HTTP 403"},
		{"upstream failure", 502, "0", "item 2 returned HTTP 502"},
		{"oversize", 200, "200", "item 2: download exceeded size limit (content length 200, remaining 100)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", tc.length)
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			_, _, err := fetchCobaltMedia(context.Background(), cobaltItem{URL: server.URL + "/?sig=private"}, Request{}, t.TempDir(), 1, 100)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private") {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestCobaltRetriesOnlyFailedGalleryItem(t *testing.T) {
	var photo bytes.Buffer
	if err := jpeg.Encode(&photo, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	var calls [16]atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			var items []cobaltItem
			for i := 0; i < 16; i++ {
				items = append(items, cobaltItem{Type: "photo", URL: server.URL + "/" + strconv.Itoa(i)})
			}
			json.NewEncoder(w).Encode(cobaltResponse{Status: "picker", Picker: items})
			return
		}
		index, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil || index < 0 || index >= 16 {
			w.WriteHeader(404)
			return
		}
		attempt := calls[index].Add(1)
		if index == 8 && attempt == 1 {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(photo.Bytes())
	}))
	defer server.Close()
	files, err := (&Cobalt{Endpoint: server.URL}).Run(context.Background(), Request{Mode: "auto"}, t.TempDir())
	if err != nil || len(files) != 16 {
		t.Fatalf("files=%d error=%v", len(files), err)
	}
	for i, file := range files {
		want := int32(1)
		if i == 8 {
			want = 2
		}
		if calls[i].Load() != want {
			t.Errorf("item %d requests=%d", i+1, calls[i].Load())
		}
		data, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(data, photo.Bytes()) {
			t.Fatalf("item %d invalid: %v", i+1, err)
		}
	}
}

func TestCobaltMediaRetryLimits(t *testing.T) {
	for _, status := range []int{403, 404, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(status) }))
			defer server.Close()
			dir := t.TempDir()
			_, _, err := fetchCobaltMedia(context.Background(), cobaltItem{URL: server.URL}, Request{}, dir, 8, 1000)
			want := int32(1)
			if status == 500 {
				want = 3
			}
			if err == nil || calls.Load() != want || !strings.Contains(err.Error(), "item 9 returned HTTP "+strconv.Itoa(status)) {
				t.Fatalf("calls=%d err=%v", calls.Load(), err)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatal("failed requests left files")
			}
		})
	}
}

func TestCobaltMediaRetryCancellation(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		w.WriteHeader(500)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	dir := t.TempDir()
	go func() {
		_, _, err := fetchCobaltMedia(ctx, cobaltItem{URL: server.URL}, Request{}, dir, 0, 1000)
		done <- err
	}()
	<-started
	// Allow the first response to finish, then cancel during backoff.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt backoff")
	}
	if calls.Load() != 1 {
		t.Fatalf("requests after cancellation: %d", calls.Load())
	}
}

func TestMediaRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		delay time.Duration
		retry bool
	}{
		{"", 0, true}, {"bad", 0, true}, {"5", 5 * time.Second, true}, {"999999999", 0, false},
		{now.Add(10 * time.Second).Format(http.TimeFormat), 10 * time.Second, true},
		{now.Add(-time.Second).Format(http.TimeFormat), 0, true},
		{now.Add(time.Minute).Format(http.TimeFormat), time.Minute, false},
	} {
		delay, retry := mediaRetryDelay(tc.value, now)
		if delay != tc.delay || retry != tc.retry {
			t.Errorf("%q: %s %v", tc.value, delay, retry)
		}
	}
}

func TestCobaltSizeErrors(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(strconv.FormatBool(chunked), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if chunked {
					w.(http.Flusher).Flush()
				}
				w.Write(append([]byte{0xff, 0xd8, 0xff}, bytes.Repeat([]byte{0}, 197)...))
			}))
			defer server.Close()
			dir := t.TempDir()
			_, _, err := fetchCobaltMedia(context.Background(), cobaltItem{URL: server.URL}, Request{}, dir, 0, 100)
			if !errors.Is(err, ErrSize) {
				t.Fatalf("wanted size error, got %v", err)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatal("oversized partial file survived")
			}
		})
	}
}
