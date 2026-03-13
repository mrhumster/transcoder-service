package processor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
)

type FFmpegProcessor struct {
	binPath string
}

func NewFFmpegProcessor() (*FFmpegProcessor, error) {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found in system: %w", err)
	}
	return &FFmpegProcessor{binPath: path}, nil
}

func (p *FFmpegProcessor) TranscodeToHLS(ctx context.Context, inputPath, outputDir string) error {
	playlistPath := fmt.Sprintf("%s/index.m3u8", outputDir)
	args := []string{
		"-i", inputPath,
		"-c:v", "libx264",
		"-c:a", "aac",
		"-b:v", "2500k",
		"-maxrate", "2500k",
		"-bufsize", "5000k",
		"-hls_time", "10",
		"-hls_list_size", "0",
		"-hls_segment_filename", fmt.Sprintf("%s/seg_%%d.ts", outputDir),
		"-f", "hls",
		playlistPath,
	}
	cmd := exec.CommandContext(ctx, p.binPath, args...)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	slog.Info("starting ffmpeg", "input", inputPath, "output", outputDir)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg start failed: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg execution failed: %w", err)
	}
	return nil
}
