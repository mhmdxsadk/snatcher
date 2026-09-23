package download

import (
	"context"
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
	for _, mode := range []string{"auto", "audio"} {
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
			if mode == "audio" && filepath.Ext(path) != ".mp3" {
				t.Fatal(path)
			}
		})
	}
}
