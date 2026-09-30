package processor

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	EncoderAuto  = "auto"
	EncoderCPU   = "cpu"
	EncoderVAAPI = "vaapi"

	vaapiDevicePath = "/dev/dri/renderD128"
)

type FFmpegProcessor struct {
	binPath string
	encoder string
}

func NewFFmpegProcessor(mode string) (*FFmpegProcessor, error) {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found in system: %w", err)
	}
	p := &FFmpegProcessor{binPath: path, encoder: EncoderCPU}
	if err := p.resolveEncoder(mode); err != nil {
		slog.Warn("transcode encoder resolution failed", "error", err)
	}
	return p, nil
}

// resolveEncoder selects the effective encoder. "auto" prefers hardware (VAAPI)
// when a render node and the h264_vaapi encoder are available, falling back to
// libx264 otherwise. "cpu" and "vaapi" force the mode; a missing capability
// degrades to software encoding instead of failing the worker.
func (p *FFmpegProcessor) resolveEncoder(mode string) error {
	switch mode {
	case EncoderVAAPI:
		if !p.canVAAPI() {
			p.encoder = EncoderCPU
			return fmt.Errorf("vaapi requested but unavailable: %s", vaapiDevicePath)
		}
		p.encoder = EncoderVAAPI
	case EncoderCPU:
		p.encoder = EncoderCPU
	case "", EncoderAuto:
		if p.canVAAPI() {
			p.encoder = EncoderVAAPI
		} else {
			p.encoder = EncoderCPU
		}
	default:
		p.encoder = EncoderCPU
		return fmt.Errorf("unknown encoder %q, falling back to %q", mode, EncoderCPU)
	}
	slog.Info("transcode encoder selected", "mode", mode, "encoder", p.encoder)
	return nil
}

func (p *FFmpegProcessor) canVAAPI() bool {
	if _, err := os.Stat(vaapiDevicePath); err != nil {
		return false
	}
	out, err := exec.CommandContext(context.Background(), p.binPath, "-hide_banner", "-encoders").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "h264_vaapi") {
			return true
		}
	}
	return false
}

func (p *FFmpegProcessor) buildArgs(inputPath, outputDir string) []string {
	playlistPath := fmt.Sprintf("%s/index.m3u8", outputDir)
	args := []string{}
	if p.encoder == EncoderVAAPI {
		args = append(args, "-vaapi_device", vaapiDevicePath)
	}
	args = append(args,
		"-i", inputPath,
		"-progress", "pipe:1",
	)
	if p.encoder == EncoderVAAPI {
		args = append(args,
			"-vf", "format=nv12,hwupload",
			"-c:v", "h264_vaapi",
		)
	} else {
		args = append(args,
			"-threads", "0",
			"-c:v", "libx264",
		)
	}
	args = append(args,
		"-c:a", "aac",
		"-b:v", "2500k",
		"-maxrate", "2500k",
		"-bufsize", "5000k",
		"-hls_time", "10",
		"-hls_list_size", "0",
		"-hls_segment_filename", fmt.Sprintf("%s/seg_%%d.ts", outputDir),
		"-f", "hls",
		playlistPath,
	)
	return args
}

// Encoder returns the currently active encoder mode ("cpu" or "vaapi").
func (p *FFmpegProcessor) Encoder() string {
	return p.encoder
}

func (p *FFmpegProcessor) TranscodeToHLS(ctx context.Context, inputPath, outputDir string) (<-chan Progress, <-chan error) {
	progChan := make(chan Progress, 10)
	errChan := make(chan error, 1)

	totalDuration, _ := p.GetDuration(ctx, inputPath)

	go func() {
		defer close(progChan)
		defer close(errChan)

		args := p.buildArgs(inputPath, outputDir)
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
		progChan <- Progress{Percent: 100, Finished: true}
		slog.Info("ffmpeg output stream closed, waiting for process to exit")

		if err := cmd.Wait(); err != nil {
			errChan <- fmt.Errorf("ffmpeg execution failed: %w", err)
		} else {
			slog.Info("ffmpeg process exited successfully")
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
	cmd := exec.CommandContext(ctx, p.probePath(), args...)
	out, err := cmd.Output()
	if err != nil {
		return 0, nil
	}
	durationStr := strings.TrimSpace(string(out))
	return strconv.ParseFloat(durationStr, 64)
}

// probePath resolves ffprobe relative to the ffmpeg binary directory.
func (p *FFmpegProcessor) probePath() string {
	probe := filepath.Join(filepath.Dir(p.binPath), "ffprobe")
	if _, err := os.Stat(probe); err == nil {
		return probe
	}
	return "ffprobe"
}
