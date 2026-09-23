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

// All media subprocesses share cancellation, including their child processes.
func mediaCommand(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

type mediaStream struct {
	CodecType   string `json:"codec_type"`
	CodecName   string `json:"codec_name"`
	PixelFormat string `json:"pix_fmt"`
}

// compatibleVideo copies compatible streams and converts the others. It writes
// a separate file so cancellation cannot publish a partly converted result.
func compatibleVideo(ctx context.Context, source string, mute bool) (string, error) {
	probe := mediaCommand(ctx, "ffprobe", "-v", "error", "-show_entries", "stream=codec_type,codec_name,pix_fmt", "-of", "json", source)
	var output boundedOutput
	probe.Stdout = &output
	probe.Stderr = io.Discard
	if err := probe.Run(); err != nil {
		return "", err
	}
	var metadata struct {
		Streams []mediaStream `json:"streams"`
	}
	if err := json.Unmarshal(output.data, &metadata); err != nil {
		return "", err
	}
	var video, audio *mediaStream
	for i := range metadata.Streams {
		stream := &metadata.Streams[i]
		if stream.CodecType == "video" && video == nil {
			video = stream
		}
		if stream.CodecType == "audio" && audio == nil {
			audio = stream
		}
	}
	if video == nil {
		return "", errors.New("source contains no video")
	}
	temp := filepath.Join(filepath.Dir(source), ".snatcher-compatible.mp4")
	defer os.Remove(temp)
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", source, "-map", "0:v:0"}
	if video.CodecName == "h264" && video.PixelFormat == "yuv420p" {
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "fast", "-crf", "20", "-pix_fmt", "yuv420p", "-vf", "pad=ceil(iw/2)*2:ceil(ih/2)*2", "-threads", "2")
	}
	if mute || audio == nil {
		args = append(args, "-an")
	} else {
		args = append(args, "-map", "0:a:0")
		if audio.CodecName == "aac" {
			args = append(args, "-c:a", "copy")
		} else {
			args = append(args, "-c:a", "aac", "-b:a", "192k")
		}
	}
	args = append(args, "-movflags", "+faststart", temp)
	cmd := mediaCommand(ctx, "ffmpeg", args...)
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	dest := strings.TrimSuffix(source, filepath.Ext(source)) + ".mp4"
	if err := os.Rename(temp, dest); err != nil {
		return "", err
	}
	if dest != source {
		if err := os.Remove(source); err != nil {
			return "", err
		}
	}
	return dest, nil
}
