package download

import (
	"context"
	"encoding/json"
	"errors"
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
	args := []string{"--ignore-config", "--no-playlist", "--no-progress", "--no-warnings", "--no-cache-dir", "--no-remote-components", "--js-runtimes", "node", "--socket-timeout", "20", "--retries", "3", "--max-filesize", "512M", "--print", "after_move:%(filepath)j", "--no-simulate", "-P", dir, "-o", "%(title).100B [%(id)s].%(ext)s"}
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
	// A collection is not a successful single-video download. Resolve its full
	// contents through the fallback, including images omitted by video extractors.
	probe := mediaCommand(ctx, y.Binary, "--ignore-config", "--no-playlist", "--flat-playlist", "--playlist-end", "21", "--no-cache-dir", "--no-remote-components", "--js-runtimes", "node", "--socket-timeout", "20", "--retries", "3", "--dump-single-json", "--", r.URL)
	var metadata metadataOutput
	probe.Stdout = &metadata
	if err := runMedia(probe); err != nil {
		if ctx.Err() != nil {
			return "", context.Cause(ctx)
		}
		return "", err
	}
	var info struct {
		Type    string          `json:"_type"`
		Entries json.RawMessage `json:"entries"`
	}
	if metadata.overflow || json.Unmarshal(metadata.data, &info) != nil {
		return "", errors.New("invalid downloader metadata")
	}
	if info.Type == "playlist" || info.Type == "multi_video" || (len(info.Entries) > 0 && string(info.Entries) != "null") {
		return "", ErrCollection
	}
	cmd := mediaCommand(ctx, y.Binary, arguments(r, dir)...)
	var out boundedOutput
	cmd.Stdout = &out
	err := runMedia(cmd)
	if ctx.Err() != nil {
		return "", context.Cause(ctx)
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
	stat, err := os.Lstat(path)
	if err != nil || !stat.Mode().IsRegular() || filepath.Dir(path) != dir {
		return "", errors.New("invalid downloader path")
	}
	if r.Mode != "audio" {
		return compatibleVideo(ctx, path, r.Mode == "mute")
	}
	return path, nil
}

// Bound extraction metadata independently of the much smaller file-path output.
type metadataOutput struct {
	data     []byte
	overflow bool
}

func (b *metadataOutput) Write(p []byte) (int, error) {
	n := len(p)
	const limit = 4 << 20
	remaining := limit - len(b.data)
	if n > remaining {
		b.overflow = true
	}
	b.data = append(b.data, p[:min(n, remaining)]...)
	return n, nil
}

var ErrCollection = errors.New("this collection requires the Cobalt fallback")
