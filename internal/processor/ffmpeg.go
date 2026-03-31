package processor

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
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

func (p *FFmpegProcessor) TranscodeToHLS(ctx context.Context, inputPath, outputDir string) (<-chan Progress, <-chan error) {
	progChan := make(chan Progress)
	errChan := make(chan error)

	totalDuration, _ := p.GetDuration(ctx, inputPath)

	go func() {
		defer close(progChan)
		defer close(errChan)

		playlistPath := fmt.Sprintf("%s/index.m3u8", outputDir)
		args := []string{
			"-i", inputPath,
			"-progress", "pipe:1",
			"-threads", "0",
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

		stdout, _ := cmd.StdoutPipe()

		slog.Info("starting ffmpeg", "input", inputPath, "output", outputDir)
		if err := cmd.Start(); err != nil {
			errChan <- fmt.Errorf("ffmpeg start failed: %w", err)
			return
		}

		scanner := bufio.NewScanner(stdout)
		currenProg := Progress{}

		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.Split(line, "=")
			if len(parts) < 2 {
				continue
			}
			key, value := parts[0], parts[1]

			switch key {
			case "frame":
				currenProg.Frames, _ = strconv.ParseInt(value, 10, 64)
			case "out_time_us":
				us, _ := strconv.ParseFloat(value, 64)
				if totalDuration > 0 {
					currenProg.Percent = (us / 1_000_000 / totalDuration) * 100
				}
			case "out_time":
				currenProg.Current = value
			case "progress":
				if value == "end" {
					currenProg.Finished = true
				}
				progChan <- currenProg
			}
		}

		if err := cmd.Wait(); err != nil {
			errChan <- fmt.Errorf("ffmpeg execution failed: %w", err)
		}
	}()
	return progChan, errChan
}

func (p *FFmpegProcessor) GetDuration(ctx context.Context, inputPath string) (float64, error) {
	args := []string{
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		inputPath,
	}
	cmd := exec.CommandContext(ctx, "ffprobe", args...)
	out, err := cmd.Output()
	if err != nil {
		return 0, nil
	}
	durationStr := strings.TrimSpace(string(out))
	return strconv.ParseFloat(durationStr, 64)
}
