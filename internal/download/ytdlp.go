package download

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type YTDLP struct{ Binary string }

func arguments(r Request, dir string) []string {
	format := "bv*+ba/b"
	if r.Quality != "max" {
		format = "bv*[height<=?" + r.Quality + "]+ba/b[height<=?" + r.Quality + "]"
	}
	if r.Mode == "mute" {
		format = "bv*"
		if r.Quality != "max" {
			format += "[height<=?" + r.Quality + "]"
		}
	}
	args := []string{"--ignore-config", "--no-playlist", "--playlist-items", "1", "--no-progress", "--no-warnings", "--no-cache-dir", "--no-remote-components", "--js-runtimes", "node", "--socket-timeout", "20", "--retries", "3", "--max-filesize", "512M", "--print", "after_move:%(filepath)j", "--no-simulate", "-P", dir, "-o", "%(title).100B [%(id)s].%(ext)s"}
	if r.Mode == "audio" {
		args = append(args, "-f", "ba/b", "-x", "--audio-format", r.AudioFormat)
	} else {
		args = append(args, "-f", format, "-S", "vcodec:h264,acodec:aac")
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
	ctx, stop := watchSize(ctx, dir)
	defer stop()
	cmd := mediaCommand(ctx, y.Binary, arguments(r, dir)...)
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out.data)), "\n")
	var path string
	if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &path) != nil {
		return "", errors.New("invalid downloader output")
	}
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || filepath.Dir(path) != dir {
		return "", errors.New("invalid downloader path")
	}
	if r.Mode != "audio" {
		return compatibleVideo(ctx, path, r.Mode == "mute")
	}
	return path, nil
}
