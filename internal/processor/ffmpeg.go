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

// chunkRange is one slice of the source for the chunked transcode path.
type chunkRange struct {
	startSec float64
	durSec   float64
}

func (p *FFmpegProcessor) buildArgs(inputPath, outputDir string) []string {
	return p.buildChunkArgs(inputPath, outputDir, chunkRange{})
}

// buildChunkArgs is the single source of ffmpeg arguments for both paths:
// with a zero chunkRange it produces exactly the original single-pass
// command line, and for a chunk it wraps the input with -ss/-t (fast seek,
// only the window is decoded) plus -output_ts_offset so the chunk lands on
// the global timeline instead of restarting at the encoder's base timestamp.
func (p *FFmpegProcessor) buildChunkArgs(inputPath, outputDir string, r chunkRange) []string {
	playlistPath := fmt.Sprintf("%s/index.m3u8", outputDir)
	args := []string{}
	if p.encoder == EncoderVAAPI {
		args = append(args, "-vaapi_device", vaapiDevicePath)
	}
	// -ss must precede -i: it becomes an input seek (an http Range request on
	// a presigned url), so a multi-hour source is not read from the start.
	if r.startSec > 0 {
		args = append(args, "-ss", strconv.FormatFloat(r.startSec, 'f', -1, 64))
	}
	args = append(args,
		"-i", inputPath,
		"-progress", "pipe:1",
	)
	if r.durSec > 0 {
		args = append(args, "-t", strconv.FormatFloat(r.durSec, 'f', -1, 64))
	}
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
	)
	if r.startSec > 0 {
		args = append(args, "-output_ts_offset", strconv.FormatFloat(r.startSec, 'f', -1, 64))
	}
	args = append(args,
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
	totalDuration, _ := p.GetDuration(ctx, inputPath)
	return p.runFFmpeg(ctx, p.buildArgs(inputPath, outputDir), inputPath, outputDir, totalDuration, nil)
}

// TranscodeChunk encodes one [startSec, startSec+durSec) slice of the source
// into outputDir. Progress.Percent and the final flush are mapped onto the
// whole video ((startSec+local)/total), never onto the chunk itself, so the
// handler can relay chunk progress as one continuous 0..100 bar.
func (p *FFmpegProcessor) TranscodeChunk(ctx context.Context, inputPath, outputDir string, startSec, durSec float64) (<-chan Progress, <-chan error) {
	totalDuration, _ := p.GetDuration(ctx, inputPath)
	r := chunkRange{startSec: startSec, durSec: durSec}
	slog.Info("starting chunk transcode",
		"input", redactURL(inputPath),
		"output", outputDir,
		"start_sec", startSec,
		"dur_sec", durSec)
	return p.runFFmpeg(ctx, p.buildChunkArgs(inputPath, outputDir, r), inputPath, outputDir, totalDuration, &r)
}

// runFFmpeg owns the ffmpeg process: it streams progress until the process
// exits, then flushes a final Progress (100 for a full pass, the chunk's
// global end-percent for a slice) and reports a non-zero exit on errChan.
// chunk is nil for the single-pass path.
func (p *FFmpegProcessor) runFFmpeg(ctx context.Context, args []string, inputPath, outputDir string, totalDuration float64, chunk *chunkRange) (<-chan Progress, <-chan error) {
	progChan := make(chan Progress, 10)
	errChan := make(chan error, 1)

	go func() {
		defer close(progChan)
		defer close(errChan)

		cmd := exec.CommandContext(ctx, p.binPath, args...)

		stdout, _ := cmd.StdoutPipe()

		slog.Info("starting ffmpeg", "input", redactURL(inputPath), "output", outputDir)
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
					local := us / 1_000_000
					if chunk != nil {
						local += chunk.startSec
					}
					currenProg.Percent = (local / totalDuration) * 100
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
		progChan <- p.finalProgress(chunk, totalDuration)
		slog.Info("ffmpeg output stream closed, waiting for process to exit")

		if err := cmd.Wait(); err != nil {
			errChan <- fmt.Errorf("ffmpeg execution failed: %w", err)
		} else {
			slog.Info("ffmpeg process exited successfully")
		}
	}()
	return progChan, errChan
}

// finalProgress is the flush pushed when the output stream closes: 100 for a
// full pass (the stream is done by definition), and for a chunk the whole
// video's percent at the chunk's end, because 100 here would falsely signal
// that every later chunk is finished.
func (p *FFmpegProcessor) finalProgress(chunk *chunkRange, totalDuration float64) Progress {
	if chunk == nil || totalDuration <= 0 {
		return Progress{Percent: 100, Finished: true}
	}
	pct := ((chunk.startSec + chunk.durSec) / totalDuration) * 100
	if pct > 100 {
		pct = 100
	}
	return Progress{Percent: pct, Finished: true}
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

// redactURL strips the query string so a presigned url's signature never
// lands in the worker logs (the input is logged on every ffmpeg run).
func redactURL(s string) string {
	if i := strings.IndexByte(s, '?'); i >= 0 {
		return s[:i] + "?[redacted]"
	}
	return s
}
