package download

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type YTDLP struct{ Binary string }

func arguments(r Request, dir string) []string {
	format := "bv*+ba/b"
	if r.Quality != "max" {
		format = "bv*[height<=?" + r.Quality + "]+ba/b[height<=?" + r.Quality + "]"
	}
	if r.Mode == "mute" {
		format = "bv"
		if r.Quality != "max" {
			format += "[height<=?" + r.Quality + "]"
		}
	}
	args := []string{"--ignore-config", "--no-playlist", "--playlist-items", "1", "--no-progress", "--no-warnings", "--no-cache-dir", "--no-remote-components", "--js-runtimes", "node", "--socket-timeout", "20", "--retries", "3", "--max-filesize", "512M", "--print", "after_move:%(filepath)j", "--no-simulate", "-P", dir, "-o", "%(title).100B [%(id)s].%(ext)s"}
	if r.Mode == "audio" {
		args = append(args, "-f", "ba/b", "-x", "--audio-format", r.AudioFormat)
	} else {
		args = append(args, "-f", format)
	}
	return append(args, "--", r.URL)
}

type boundedOutput struct{ data []byte }

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.data) < 16384 {
		b.data = append(b.data, p[:min(len(p), 16384-len(b.data))]...)
	}
	return n, nil
}
func (y YTDLP) Run(ctx context.Context, r Request, dir string) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, y.Binary, arguments(r, dir)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	// Bound partial files too, including formats without Content-Length.
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
				filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
					if err == nil && !d.IsDir() {
						if i, e := d.Info(); e == nil {
							size += i.Size()
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
	err := cmd.Run()
	contextErr := ctx.Err()
	cancel()
	<-done
	if contextErr != nil {
		return "", contextErr
	}
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out.data)), "\n")
	var path string
	if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &path) != nil {
		return "", errors.New("invalid downloader output")
	}
	return filepath.Clean(path), nil
}
