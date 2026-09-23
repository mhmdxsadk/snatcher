package download

import (
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
