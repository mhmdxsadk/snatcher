package download

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type videoFunc func(context.Context, Request, string) (string, error)

func (f videoFunc) Run(ctx context.Context, r Request, dir string) (string, error) {
	return f(ctx, r, dir)
}

func TestPipelineRouting(t *testing.T) {
	for _, tc := range []struct {
		name, body, mode  string
		fallback, success bool
	}{
		{"unsupported", "exit 3", "auto", true, true},
		{"discovery-error", "exit 1", "auto", true, true},
		{"partial-gallery", "exit 4", "auto", false, false},
		{"invalid-manifest", "echo invalid", "auto", false, false},
		{"audio", "exit 4", "audio", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "python")
			if err := os.WriteFile(script, []byte("#!/bin/sh\n"+tc.body+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			called := false
			p := Pipeline{Python: script, Video: videoFunc(func(context.Context, Request, string) (string, error) { called = true; return "video.mp4", nil })}
			_, err := p.Run(context.Background(), Request{Mode: tc.mode}, t.TempDir())
			if called != tc.fallback || (err == nil) != tc.success {
				t.Fatalf("fallback %v error %v", called, err)
			}
		})
	}
}

func TestGalleryLoginErrorSurvivesVideoFailure(t *testing.T) {
	script := filepath.Join(t.TempDir(), "python")
	os.WriteFile(script, []byte("#!/bin/sh\necho 'HTTP redirect to login page' >&2\nexit 1\n"), 0700)
	p := Pipeline{Python: script, Video: videoFunc(func(context.Context, Request, string) (string, error) { return "", errors.New("video failed") })}
	_, err := p.Run(context.Background(), Request{Mode: "auto"}, t.TempDir())
	if !errors.Is(err, ErrGalleryLogin) {
		t.Fatal(err)
	}
}
