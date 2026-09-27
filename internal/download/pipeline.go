package download

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxJobFiles = 20

var ErrSize = errors.New("download exceeded size limit")

type Pipeline struct {
	Video interface {
		Run(context.Context, Request, string) (string, error)
	}
	Fallback Runner
}

func (p Pipeline) Run(ctx context.Context, r Request, dir string) ([]string, error) {
	file, err := p.Video.Run(ctx, r, dir)
	if err == nil {
		return []string{file}, nil
	}
	if p.Fallback == nil || ctx.Err() != nil || errors.Is(err, ErrSize) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	// Remove partial primary output before sharing the job's budget with fallback.
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		return nil, readErr
	}
	for _, entry := range entries {
		if cleanErr := os.RemoveAll(filepath.Join(dir, entry.Name())); cleanErr != nil {
			return nil, cleanErr
		}
	}
	files, fallbackErr := p.Fallback.Run(ctx, r, dir)
	if fallbackErr != nil {
		if errors.Is(err, ErrCollection) {
			return nil, fmt.Errorf("collection fallback failed: %w", fallbackErr)
		}
		return nil, errors.Join(err, fallbackErr)
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
	ctx, cancel := context.WithCancelCause(ctx)
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
					cancel(ErrSize)
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(nil); <-done }
}
