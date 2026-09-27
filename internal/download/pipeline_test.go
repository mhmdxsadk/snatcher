package download

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type videoFunc func(context.Context, Request, string) (string, error)

func (f videoFunc) Run(c context.Context, r Request, d string) (string, error) { return f(c, r, d) }
func TestPipelineRouting(t *testing.T) {
	for _, tc := range []struct {
		name     string
		primary  error
		fallback bool
	}{{"success", nil, false}, {"failure", errors.New("failed"), true}, {"cancelled", context.Canceled, false}, {"deadline", context.DeadlineExceeded, false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			called := false
			p := Pipeline{Video: videoFunc(func(context.Context, Request, string) (string, error) {
				os.WriteFile(filepath.Join(dir, "partial"), []byte("partial"), 0600)
				return "video.mp4", tc.primary
			}), Fallback: filesRunner(func(context.Context, Request, string) ([]string, error) {
				called = true
				entries, _ := os.ReadDir(dir)
				if len(entries) != 0 {
					t.Fatal("partial files survived fallback")
				}
				return []string{"001.jpg"}, nil
			})}
			_, err := p.Run(context.Background(), Request{}, dir)
			if called != tc.fallback {
				t.Fatalf("fallback %v", called)
			}
			if tc.primary == nil && err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestPipelineDisabledFallback(t *testing.T) {
	want := errors.New("primary failure")
	p := Pipeline{Video: videoFunc(func(context.Context, Request, string) (string, error) { return "", want })}
	_, err := p.Run(context.Background(), Request{}, t.TempDir())
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func TestCollectionFallbackError(t *testing.T) {
	want := errors.New("upstream unavailable")
	p := Pipeline{Video: videoFunc(func(context.Context, Request, string) (string, error) { return "", ErrCollection }), Fallback: filesRunner(func(context.Context, Request, string) ([]string, error) { return nil, want })}
	_, err := p.Run(context.Background(), Request{}, t.TempDir())
	if !errors.Is(err, want) || errors.Is(err, ErrCollection) {
		t.Fatalf("misleading fallback diagnostic: %v", err)
	}
}
