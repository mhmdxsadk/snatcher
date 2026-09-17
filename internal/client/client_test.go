package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestResponseSizeLimit(t *testing.T) {
	const body = `{"status":"redirect","url":"https://media.example/video.mp4"}`
	for _, size := range []int{maxResponseBytes, maxResponseBytes + 1} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body + strings.Repeat(" ", size-len(body))))
		}))
		c, err := New(upstream.URL)
		if err != nil {
			upstream.Close()
			t.Fatal(err)
		}
		_, err = c.Resolve(context.Background(), "https://example.com/video", Options{})
		upstream.Close()
		if size == maxResponseBytes && err != nil {
			t.Fatalf("response at limit was rejected: %v", err)
		}
		if size > maxResponseBytes && (err == nil || !strings.Contains(err.Error(), "exceeds")) {
			t.Fatalf("oversized response: got %v", err)
		}
	}
}

func TestHTTPRedirectNotFollowed(t *testing.T) {
	var followed atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Store(true)
	}))
	defer destination.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	c, err := New(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Resolve(context.Background(), "https://example.com/video", Options{})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("expected HTTP redirect error, got %v", err)
	}
	if followed.Load() {
		t.Fatal("forwarded the request to an HTTP redirect destination")
	}
}

func TestCancellation(t *testing.T) {
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	c, err := New(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.Resolve(ctx, "https://example.com/video", Options{})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request did not stop after cancellation")
	}
}
