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
	"sync"
	"time"
)

const (
	EncoderAuto  = "auto"
	EncoderCPU   = "cpu"
	EncoderVAAPI = "vaapi"

	vaapiDevicePath = "/dev/dri/renderD128"

	// ffmpegStderrTail bounds how much ffmpeg stderr is retained for error
	// reporting: the periodic stats line would otherwise grow without bound
	// over a multi-hour transcode.
	ffmpegStderrTail = 4096
	// vaapiProbeTimeout caps the one-frame hardware probe run at startup.
	vaapiProbeTimeout = 20 * time.Second
)

type FFmpegProcessor struct {
	binPath string
	encoder string
	mu      sync.Mutex
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

// canVAAPI reports whether hardware encoding is actually usable, not merely
// advertised. A render node plus an h264_vaapi entry in `-encoders` is not
// enough: the userspace VA driver must be able to create a display, which only
// happens when the container ships the matching libva/dri bits. We therefore
// encode a single throwaway frame to the null muxer and treat any non-zero exit
// as "no hardware", so the worker degrades to software instead of failing.
func (p *FFmpegProcessor) canVAAPI() bool {
	if _, err := os.Stat(vaapiDevicePath); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), vaapiProbeTimeout)
	defer cancel()
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-vaapi_device", vaapiDevicePath,
		"-f", "lavfi", "-i", "testsrc=size=1280x720:rate=30:duration=1",
		"-vf", "format=nv12,hwupload",
		"-c:v", "h264_vaapi",
		"-frames:v", "1",
		"-f", "null", "-",
	}
	if out, err := exec.CommandContext(ctx, p.binPath, args...).CombinedOutput(); err != nil {
		slog.Warn("vaapi probe failed, hardware unavailable",
			"device", vaapiDevicePath,
			"error", err,
			"output", strings.TrimSpace(string(out)))
		return false
	}
	return true
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
	return p.buildChunkArgsFor(p.Encoder(), inputPath, outputDir, r)
}

// buildChunkArgsFor is buildChunkArgs with an explicit encoder, so a run can
// be retried on a different encoder without mutating shared state first.
func (p *FFmpegProcessor) buildChunkArgsFor(encoder, inputPath, outputDir string, r chunkRange) []string {
	playlistPath := fmt.Sprintf("%s/index.m3u8", outputDir)
	args := []string{}
	if encoder == EncoderVAAPI {
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
	if encoder == EncoderVAAPI {
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
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.encoder
}

// encoderCandidates is the ordered list of encoders one run may try: software
// is always the last resort, hardware only ever leads it.
func (p *FFmpegProcessor) encoderCandidates() []string {
	enc := p.Encoder()
	if enc == EncoderVAAPI {
		return []string{EncoderVAAPI, EncoderCPU}
	}
	return []string{enc}
}

// demoteToCPU pins the processor to software after a hardware attempt failed
// before producing a single frame, so later chunks of the same job (and the
// next job) do not repeat the same failed probe.
func (p *FFmpegProcessor) demoteToCPU() {
	p.mu.Lock()
	if p.encoder == EncoderVAAPI {
		p.encoder = EncoderCPU
	}
	p.mu.Unlock()
}

func (p *FFmpegProcessor) TranscodeToHLS(ctx context.Context, inputPath, outputDir string) (<-chan Progress, <-chan error) {
	totalDuration, _ := p.GetDuration(ctx, inputPath)
	return p.runFFmpeg(ctx, inputPath, outputDir, totalDuration, nil)
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
	return p.runFFmpeg(ctx, inputPath, outputDir, totalDuration, &r)
}

// runFFmpeg owns the ffmpeg process: it streams progress until the process
// exits, then flushes a final Progress (100 for a full pass, the chunk's
// global end-percent for a slice) and reports a non-zero exit on errChan.
// chunk is nil for the single-pass path.
//
// When the active encoder is hardware and the run dies before encoding a
// single frame (the classic "device is present but VAAPI is unusable"
// failure), the processor is demoted to software and the run is retried once
// on CPU. This keeps a broken /dev/dri from failing the whole worker.
func (p *FFmpegProcessor) runFFmpeg(ctx context.Context, inputPath, outputDir string, totalDuration float64, chunk *chunkRange) (<-chan Progress, <-chan error) {
	progChan := make(chan Progress, 10)
	errChan := make(chan error, 1)

	go func() {
		defer close(progChan)
		defer close(errChan)

		candidates := p.encoderCandidates()
		for i, encoder := range candidates {
			madeProgress, err := p.runFFmpegOnce(ctx, encoder, inputPath, outputDir, totalDuration, chunk, progChan)
			if err == nil {
				progChan <- p.finalProgress(chunk, totalDuration)
				slog.Info("ffmpeg process exited successfully", "encoder", encoder)
				return
			}
			if ctx.Err() == nil && encoder == EncoderVAAPI && !madeProgress && i+1 < len(candidates) {
				slog.Warn("vaapi produced no frames before failing; retrying with software encoding", "error", err)
				p.demoteToCPU()
				if clearErr := clearDir(outputDir); clearErr != nil {
					slog.Warn("could not clear output dir before software retry", "dir", outputDir, "error", clearErr)
				}
				continue
			}
			errChan <- err
			return
		}
	}()
	return progChan, errChan
}

// runFFmpegOnce runs a single ffmpeg invocation for one encoder. It reports
// whether any frame was encoded, which the caller uses to tell a hardware
// initialisation failure apart from a genuine transcode error.
func (p *FFmpegProcessor) runFFmpegOnce(ctx context.Context, encoder, inputPath, outputDir string, totalDuration float64, chunk *chunkRange, progChan chan<- Progress) (bool, error) {
	var r chunkRange
	if chunk != nil {
		r = *chunk
	}
	args := p.buildChunkArgsFor(encoder, inputPath, outputDir, r)

	cmd := exec.CommandContext(ctx, p.binPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, fmt.Errorf("ffmpeg stdout pipe: %w", err)
	}

	// ffmpeg writes human-readable errors (and the periodic stats banner) to
	// stderr; a bounded tail is what turns a bare "exit status 1" into an
	// actionable message.
	stderr := &tailBuffer{max: ffmpegStderrTail}
	cmd.Stderr = stderr

	slog.Info("starting ffmpeg", "input", redactURL(inputPath), "output", outputDir, "encoder", encoder)
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("ffmpeg start failed: %w", err)
	}

	madeProgress := false
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
			if currenProg.Frames > 0 {
				madeProgress = true
			}
		case "out_time_us":
			us, _ := strconv.ParseFloat(value, 64)
			if us > 0 {
				madeProgress = true
			}
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
	slog.Info("ffmpeg output stream closed, waiting for process to exit", "encoder", encoder)

	if err := cmd.Wait(); err != nil {
		if tail := stderr.String(); tail != "" {
			return madeProgress, fmt.Errorf("ffmpeg execution failed: %w: %s", err, tail)
		}
		return madeProgress, fmt.Errorf("ffmpeg execution failed: %w", err)
	}
	return madeProgress, nil
}

// tailBuffer is an io.Writer that retains only the last max bytes written, so
// ffmpeg's periodic stats output cannot grow a transcode's memory use.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if t.max <= 0 {
		return n, nil
	}
	if len(p) >= t.max {
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
	} else {
		t.buf = append(t.buf, p...)
		if len(t.buf) > t.max {
			t.buf = t.buf[:copy(t.buf, t.buf[len(t.buf)-t.max:])]
		}
	}
	return n, nil
}

func (t *tailBuffer) String() string {
	return strings.TrimSpace(string(t.buf))
}

// clearDir removes the contents of dir (not the dir) so a retry starts clean.
func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
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
