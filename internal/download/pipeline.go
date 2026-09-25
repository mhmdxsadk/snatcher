package download

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxJobFiles = 20

var ErrGalleryLogin = errors.New("gallery source requires authentication")

//go:embed gallery.py
var galleryScript string

// Pipeline isolates the gallery backend from the job and HTTP contracts.
// Audio uses yt-dlp directly. Gallery discovery can fall back to yt-dlp;
// once a photo gallery is identified, download failures never publish a subset.
type Pipeline struct {
	Video interface {
		Run(context.Context, Request, string) (string, error)
	}
	Python string
}

func (p Pipeline) Run(ctx context.Context, r Request, dir string) ([]string, error) {
	var galleryErr error
	if r.Mode != "audio" {
		files, err := p.gallery(ctx, r, dir)
		if err == nil {
			return files, nil
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		var exit *exec.ExitError
		if !errors.Is(err, ErrGalleryLogin) && (!errors.As(err, &exit) || (exit.ExitCode() != 1 && exit.ExitCode() != 3)) {
			return nil, err
		}
		galleryErr = err
	}
	file, err := p.Video.Run(ctx, r, dir)
	if err != nil {
		return nil, errors.Join(galleryErr, err)
	}
	return []string{file}, nil
}

func (p Pipeline) gallery(ctx context.Context, r Request, dir string) ([]string, error) {
	ctx, stop := watchSize(ctx, dir)
	defer stop()
	request, _ := json.Marshal(map[string]string{"url": r.URL, "quality": r.Quality})
	cmd := mediaCommand(ctx, p.Python, "-c", galleryScript, string(request), dir, fmt.Sprint(maxJobFiles))
	var out, diagnostic boundedOutput
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		message := strings.ToLower(string(diagnostic.data))
		if strings.Contains(message, "login") || strings.Contains(message, "sign in") || strings.Contains(message, "authentication") {
			return nil, ErrGalleryLogin
		}
		return nil, fmt.Errorf("gallery-dl: %w", err)
	}
	var files []string
	if err := json.Unmarshal(out.data, &files); err != nil {
		return nil, errors.New("invalid gallery output")
	}
	if len(files) == 0 || len(files) > maxJobFiles {
		return nil, errors.New("invalid gallery file count")
	}
	seen := map[string]bool{}
	for i, path := range files {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || filepath.Dir(path) != dir || seen[path] {
			return nil, errors.New("invalid gallery path")
		}
		seen[path] = true
		if MediaType(path, "video") != "photo" {
			files[i], err = compatibleVideo(ctx, path, r.Mode == "mute")
			if err != nil {
				return nil, err
			}
		}
	}
	return files, nil
}

func MediaType(path, fallback string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif":
		return "photo"
	}
	return fallback
}

// The limit covers all gallery files and conversion intermediates together.
func watchSize(ctx context.Context, dir string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				var size int64
				filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
					if err == nil && !entry.IsDir() {
						if info, err := entry.Info(); err == nil {
							size += info.Size()
						}
					}
					return nil
				})
				if size > maxJobBytes {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(); <-done }
}
