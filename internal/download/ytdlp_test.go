package download

import (
	"errors"

	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestYTDLPIntegration(t *testing.T) {
	if os.Getenv("SNATCHER_INTEGRATION") != "1" {
		t.Skip("set SNATCHER_INTEGRATION=1 to exercise yt-dlp and ffmpeg")
	}
	binary, err := exec.LookPath("yt-dlp")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=32x32:d=1", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:v", "libx264", "-c:a", "aac", "-shortest", filepath.Join(source, "test.mp4")).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(source)))
	defer server.Close()
	for _, mode := range []string{"auto", "audio", "mute"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			path, err := (YTDLP{Binary: binary}).Run(ctx, Request{URL: server.URL + "/test.mp4", Quality: "1080", Mode: mode, AudioFormat: "mp3"}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Size() == 0 {
				t.Fatalf("output: %v", err)
			}
			if mode != "audio" {
				assertVideo(t, path, mode == "mute")
			}
			if mode == "audio" && filepath.Ext(path) != ".mp3" {
				t.Fatal(path)
			}
		})
	}
}

func assertVideo(t *testing.T, path string, mute bool) {
	t.Helper()
	if filepath.Ext(path) != ".mp4" {
		t.Fatalf("expected MP4: %s", path)
	}
	data, err := exec.Command("ffprobe", "-v", "error", "-show_streams", "-of", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var result struct{ Streams []mediaStream }
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	video, audio := 0, 0
	for _, s := range result.Streams {
		if s.CodecType == "video" {
			video++
			if s.CodecName != "h264" || s.PixelFormat != "yuv420p" {
				t.Fatalf("incompatible video: %+v", s)
			}
		}
		if s.CodecType == "audio" {
			audio++
			if s.CodecName != "aac" {
				t.Fatalf("incompatible audio: %+v", s)
			}
		}
	}
	if video != 1 || (mute && audio != 0) || (!mute && audio != 1) {
		t.Fatalf("unexpected streams: %+v", result.Streams)
	}
}

func TestVideoConversionIntegration(t *testing.T) {
	if os.Getenv("SNATCHER_INTEGRATION") != "1" {
		t.Skip("set SNATCHER_INTEGRATION=1")
	}
	for _, ext := range []string{".webm", ".mp4"} {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source"+ext)
			codec, audio := "libvpx-vp9", "libopus"
			if ext == ".mp4" {
				codec, audio = "mpeg4", "aac"
			}
			data, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=32x32:d=1", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:v", codec, "-c:a", audio, "-shortest", source).CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, data)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err := compatibleVideo(ctx, source, false)
			if err != nil {
				t.Fatal(err)
			}
			assertVideo(t, result, false)
		})
	}
}

func TestCollectionUsesFallbackIntegration(t *testing.T) {
	if os.Getenv("SNATCHER_INTEGRATION") != "1" {
		t.Skip("set SNATCHER_INTEGRATION=1")
	}
	root := t.TempDir()
	out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=32x32:d=1", "-c:v", "libx264", filepath.Join(root, "a.mp4")).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.mp4"))
	os.WriteFile(filepath.Join(root, "b.mp4"), data, 0600)
	os.WriteFile(filepath.Join(root, "index.html"), []byte(`<html><title>Collection</title><video src="a.mp4"></video><video src="b.mp4"></video></html>`), 0600)
	server := httptest.NewServer(http.FileServer(http.Dir(root)))
	defer server.Close()
	req := Request{URL: server.URL + "/index.html", Quality: "1080", Mode: "auto", AudioFormat: "mp3"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fallbackCalled := false
	p := Pipeline{Video: YTDLP{Binary: "yt-dlp"}, Fallback: filesRunner(func(_ context.Context, _ Request, dir string) ([]string, error) {
		fallbackCalled = true
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatal("primary downloaded partial collection")
		}
		return []string{"first.mp4", "second.jpg"}, nil
	})}
	files, err := p.Run(ctx, req, t.TempDir())
	if err != nil || !fallbackCalled || len(files) != 2 {
		t.Fatalf("%v %v %v", files, err, fallbackCalled)
	}
	_, err = (Pipeline{Video: YTDLP{Binary: "yt-dlp"}}).Run(ctx, req, t.TempDir())
	if !errors.Is(err, ErrCollection) {
		t.Fatalf("unconfigured fallback: %v", err)
	}
}
