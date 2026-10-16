package processor

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func newTestProcessor(encoder string) *FFmpegProcessor {
	return &FFmpegProcessor{binPath: "/usr/bin/ffmpeg", encoder: encoder}
}

func TestBuildArgs_CPU(t *testing.T) {
	p := newTestProcessor(EncoderCPU)
	// 1080p source: the cap must follow the resolution, not a fixed 2500k.
	args := p.buildArgs("/in/video.mp4", "/out/dir", resolution{width: 1920, height: 1080})

	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-threads", "0",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "20",
		"-c:a", "aac",
		"-maxrate", "8000k",
		"-bufsize", "12000k",
		"-hls_time", "10",
		"-hls_list_size", "0",
		"-f", "hls",
	} {
		if !containsArg(args, want) {
			t.Errorf("cpu args missing %q", want)
		}
	}
	if strings.Contains(joined, "-b:v") {
		t.Errorf("cpu args must not use a fixed -b:v: %s", joined)
	}
	if strings.Contains(joined, "scale") {
		t.Errorf("cpu args must not scale the source: %s", joined)
	}
	if !strings.Contains(joined, "-hls_segment_filename "+"/out/dir/seg_%d.ts") {
		t.Errorf("hls segment filename wrong: %s", joined)
	}
	if strings.Contains(joined, "vaapi") {
		t.Errorf("cpu args must not contain vaapi: %s", joined)
	}
}

func TestBuildArgs_VAAPI(t *testing.T) {
	p := newTestProcessor(EncoderVAAPI)
	args := p.buildArgs("/in/video.mp4", "/out/dir", resolution{width: 1280, height: 720})

	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-vaapi_device", vaapiDevicePath,
		"-c:v", "h264_vaapi",
		"-vf", "format=nv12,hwupload",
		"-c:a", "aac",
		"-qp", "23",
		"-maxrate", "5000k",
		"-bufsize", "7500k",
		"-f", "hls",
	} {
		if !containsArg(args, want) {
			t.Errorf("vaapi args missing %q", want)
		}
	}
	// -vaapi_device must precede the input so ffmpeg binds it before decoding.
	devIdx, inputIdx := -1, -1
	for i, a := range args {
		switch a {
		case "-vaapi_device":
			devIdx = i
		case "-i":
			inputIdx = i
		}
	}
	if devIdx == -1 || inputIdx == -1 || devIdx > inputIdx {
		t.Errorf("-vaapi_device must come before -i: %s", joined)
	}
	if strings.Contains(joined, "-threads") {
		t.Errorf("vaapi args must not contain -threads: %s", joined)
	}
	if strings.Contains(joined, "-crf") {
		t.Errorf("vaapi has no -crf, must use -qp: %s", joined)
	}
}

func TestResolveEncoder_Forced(t *testing.T) {
	p := newTestProcessor("")
	if err := p.resolveEncoder(EncoderCPU); err != nil {
		t.Fatalf("cpu resolve: %v", err)
	}
	if p.encoder != EncoderCPU {
		t.Fatalf("expected cpu, got %s", p.encoder)
	}
}

func TestResolveEncoder_Unknown(t *testing.T) {
	p := newTestProcessor("")
	if err := p.resolveEncoder("quic-sync"); err == nil {
		t.Fatal("expected error for unknown encoder")
	}
	if p.encoder != EncoderCPU {
		t.Fatalf("expected cpu fallback, got %s", p.encoder)
	}
}

func TestResolveEncoder_AutoWithoutGPU(t *testing.T) {
	// Without a render node there is no hardware path (guard on the stat check).
	if _, err := os.Stat(vaapiDevicePath); err == nil {
		t.Skip("render node present on this machine, auto picks hardware")
	}
	p := newTestProcessor("")
	if err := p.resolveEncoder(EncoderAuto); err != nil {
		t.Fatalf("auto resolve: %v", err)
	}
	if p.encoder != EncoderCPU {
		t.Fatalf("expected cpu fallback without GPU, got %s", p.encoder)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestBuildChunkArgs_ChunkedFlags(t *testing.T) {
	p := newTestProcessor(EncoderCPU)
	args := p.buildChunkArgs("/in/video.mp4", "/out/dir", chunkRange{startSec: 1800, durSec: 1800}, resolution{width: 1920, height: 1080})

	idxOf := func(flag string) int {
		for i, a := range args {
			if a == flag {
				return i
			}
		}
		return -1
	}
	ssIdx, iIdx, tIdx, offIdx := idxOf("-ss"), idxOf("-i"), idxOf("-t"), idxOf("-output_ts_offset")
	if ssIdx < 0 || iIdx < 0 || tIdx < 0 || offIdx < 0 {
		t.Fatalf("chunk args missing a range flag: %s", strings.Join(args, " "))
	}
	// -ss must precede -i (input seek), -t and -output_ts_offset must follow it.
	if ssIdx >= iIdx || iIdx >= tIdx || tIdx >= offIdx {
		t.Fatalf("wrong flag order: %s", strings.Join(args, " "))
	}
	if args[ssIdx+1] != "1800" || args[tIdx+1] != "1800" || args[offIdx+1] != "1800" {
		t.Fatalf("flag values wrong: %s", strings.Join(args, " "))
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-threads", "0", "-c:v", "libx264", "-f", "hls"} {
		if !containsArg(args, want) {
			t.Errorf("chunk args missing %q", want)
		}
	}
	if strings.Contains(joined, "vaapi") {
		t.Errorf("cpu chunk args must not contain vaapi: %s", joined)
	}
}

func TestBuildChunkArgs_VAAPIChunk(t *testing.T) {
	p := newTestProcessor(EncoderVAAPI)
	args := p.buildChunkArgs("/in/video.mp4", "/out/dir", chunkRange{startSec: 60, durSec: 30}, resolution{width: 1920, height: 1080})
	joined := strings.Join(args, " ")

	if !containsArg(args, "-vaapi_device") || !containsArg(args, "h264_vaapi") {
		t.Fatalf("vaapi chunk args incomplete: %s", joined)
	}
	if containsArg(args, "-threads") {
		t.Fatalf("vaapi args must not contain -threads: %s", joined)
	}
	// -vaapi_device still binds before everything else, -ss before -i.
	devIdx, ssIdx, iIdx := -1, -1, -1
	for i, a := range args {
		switch a {
		case "-vaapi_device":
			devIdx = i
		case "-ss":
			ssIdx = i
		case "-i":
			iIdx = i
		}
	}
	if devIdx < 0 || ssIdx < 0 || iIdx < 0 || devIdx >= ssIdx || ssIdx >= iIdx {
		t.Fatalf("flag order wrong: %s", joined)
	}
}

// TestBuildArgs_MatchesFirstChunk pins the single-pass command to the
// zero chunk: buildArgs is now a thin wrapper, and a drift here would
// silently change every short-video transcode.
func TestBuildArgs_MatchesFirstChunk(t *testing.T) {
	res := resolution{width: 1920, height: 1080}
	for _, enc := range []string{EncoderCPU, EncoderVAAPI} {
		p := newTestProcessor(enc)
		single := strings.Join(p.buildArgs("/in/video.mp4", "/out/dir", res), "\x00")
		first := strings.Join(p.buildChunkArgs("/in/video.mp4", "/out/dir", chunkRange{}, res), "\x00")
		if single != first {
			t.Fatalf("%s: single-pass args drifted from the zero chunk:\nsingle: %s\nchunk:  %s",
				enc, strings.Join(p.buildArgs("/in/video.mp4", "/out/dir", res), " "), strings.Join(p.buildChunkArgs("/in/video.mp4", "/out/dir", chunkRange{}, res), " "))
		}
	}
}

func TestBitrateCapFor_Ladder(t *testing.T) {
	cases := []struct {
		name    string
		res     resolution
		maxrate string
		bufsize string
	}{
		{"unknown", resolution{}, "8000k", "12000k"},
		{"480p", resolution{width: 640, height: 480}, "2500k", "5000k"},
		{"720p", resolution{width: 1280, height: 720}, "5000k", "7500k"},
		{"1080p", resolution{width: 1920, height: 1080}, "8000k", "12000k"},
		{"portrait-1080p", resolution{width: 1080, height: 1920}, "8000k", "12000k"},
		{"1440p", resolution{width: 2560, height: 1440}, "14000k", "21000k"},
		{"4k", resolution{width: 3840, height: 2160}, "24000k", "36000k"},
	}
	for _, tc := range cases {
		maxrate, bufsize := bitrateCapFor(tc.res)
		if maxrate != tc.maxrate || bufsize != tc.bufsize {
			t.Errorf("%s: got %s/%s, want %s/%s", tc.name, maxrate, bufsize, tc.maxrate, tc.bufsize)
		}
	}
}

func TestQualityArgs_Encoders(t *testing.T) {
	cpu := qualityArgs(EncoderCPU, resolution{width: 1920, height: 1080})
	for _, want := range []string{"-preset", libx264Preset, "-crf", libx264CRF, "-maxrate", "8000k", "-bufsize", "12000k"} {
		if !containsArg(cpu, want) {
			t.Errorf("cpu quality args missing %q: %v", want, cpu)
		}
	}
	vaapi := qualityArgs(EncoderVAAPI, resolution{width: 1920, height: 1080})
	if containsArg(vaapi, "-crf") || containsArg(vaapi, "-preset") {
		t.Errorf("vaapi quality args must not carry crf/preset: %v", vaapi)
	}
	for _, want := range []string{"-qp", vaapiQP, "-maxrate", "8000k", "-bufsize", "12000k"} {
		if !containsArg(vaapi, want) {
			t.Errorf("vaapi quality args missing %q: %v", want, vaapi)
		}
	}
}

func TestFinalProgress(t *testing.T) {
	p := newTestProcessor(EncoderCPU)

	// A full pass flushes 100 — the stream is done by definition.
	full := p.finalProgress(nil, 5400)
	if full.Percent != 100 || !full.Finished {
		t.Fatalf("full pass final = %+v, want 100/finished", full)
	}

	// A chunk flushes its GLOBAL end percent, never 100: 1800+1800 of 5400.
	chunk := p.finalProgress(&chunkRange{startSec: 1800, durSec: 1800}, 5400)
	want := (3600.0 / 5400.0) * 100
	if math.Abs(chunk.Percent-want) > 0.01 || !chunk.Finished {
		t.Fatalf("chunk final = %+v, want %.2f/finished", chunk, want)
	}

	// A rounding overshoot past the source duration clamps at 100.
	over := p.finalProgress(&chunkRange{startSec: 0, durSec: 200}, 100)
	if over.Percent != 100 {
		t.Fatalf("overshoot final = %+v, want clamp to 100", over)
	}
}

// writeFakeFFmpeg drops an executable stand-in for the ffmpeg binary and
// returns its path. The script receives the real argument vector, so tests can
// branch on flags (e.g. "-vaapi_device") without any hardware.
func writeFakeFFmpeg(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunFFmpeg_ReportsStderr pins the diagnostic contract: ffmpeg's stderr
// must survive into the returned error instead of a bare "exit status 1".
func TestRunFFmpeg_ReportsStderr(t *testing.T) {
	p := &FFmpegProcessor{
		binPath: writeFakeFFmpeg(t, "echo 'boom: no such filter' >&2\nexit 1\n"),
		encoder: EncoderCPU,
	}
	outDir := t.TempDir()

	progChan, errChan := p.TranscodeToHLS(context.Background(), "/in/video.mp4", outDir)
	drainProgress(progChan)

	var got error
	for err := range errChan {
		got = err
	}
	if got == nil {
		t.Fatal("expected a transcode error")
	}
	if !strings.Contains(got.Error(), "boom: no such filter") {
		t.Fatalf("error lost ffmpeg stderr: %v", got)
	}
}

// TestRunFFmpeg_FallsBackToCPU covers the VAAPI-as-broken-hardware path: the
// first attempt dies before a single frame, so the run must retry on software
// and leave the processor pinned to cpu.
func TestRunFFmpeg_FallsBackToCPU(t *testing.T) {
	script := `for a in "$@"; do
  if [ "$a" = "-vaapi_device" ]; then
    echo "No VA display found for device" >&2
    exit 1
  fi
done
echo "frame=1"
echo "out_time_us=1000000"
echo "progress=end"
exit 0
`
	p := &FFmpegProcessor{binPath: writeFakeFFmpeg(t, script), encoder: EncoderVAAPI}
	outDir := t.TempDir()

	progChan, errChan := p.TranscodeToHLS(context.Background(), "/in/video.mp4", outDir)
	drainProgress(progChan)

	for err := range errChan {
		t.Fatalf("expected software retry to succeed, got: %v", err)
	}
	if got := p.Encoder(); got != EncoderCPU {
		t.Fatalf("processor not demoted to cpu, got %s", got)
	}
}

func drainProgress(ch <-chan Progress) {
	for range ch {
	}
}

// TestTranscodeChunk_GlobalProgress runs a real ffmpeg slice and checks the
// contract the chunked handler relies on: the output covers durSec of the
// source and every percent (including the final flush) lives on the whole
// video's scale, not the chunk's.
func TestTranscodeChunk_GlobalProgress(t *testing.T) {
	p := newTestMuxer(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "source.mp4")
	run(t, p.binPath,
		"-v", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=15:duration=16",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=16",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "15",
		"-c:a", "aac", "-b:a", "64k",
		"-shortest",
		src,
	)

	outDir := filepath.Join(dir, "chunk")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Slice [8, 14) of a 16s source: global percents must span ~50..87.5.
	progChan, errChan := p.TranscodeChunk(context.Background(), src, outDir, 8, 6)

	var last Progress
	var events int
	var taskErr error
	for progChan != nil || errChan != nil {
		select {
		case prog, ok := <-progChan:
			if !ok {
				progChan = nil
				continue
			}
			events++
			if prog.Percent < 49 {
				t.Fatalf("event %d percent %.2f below the chunk's global start (50)", events, prog.Percent)
			}
			if prog.Percent > 88 {
				t.Fatalf("event %d percent %.2f above the chunk's global end (87.5)", events, prog.Percent)
			}
			last = prog
		case err, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			taskErr = err
		}
	}
	if taskErr != nil {
		t.Fatalf("TranscodeChunk: %v", taskErr)
	}
	if events == 0 {
		t.Fatal("no progress events received")
	}
	if !last.Finished {
		t.Fatalf("last progress is not the final flush: %+v", last)
	}
	if math.Abs(last.Percent-87.5) > 1.5 {
		t.Fatalf("final percent %.2f, want ~87.5 ((8+6)/16)", last.Percent)
	}

	// The chunk playlist must cover exactly durSec.
	playlist := filepath.Join(outDir, "index.m3u8")
	if _, err := os.Stat(playlist); err != nil {
		t.Fatalf("chunk playlist missing: %v", err)
	}
	out, err := exec.Command(p.probePath(),
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		playlist).Output()
	if err != nil {
		t.Fatalf("ffprobe chunk playlist: %v", err)
	}
	dur, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		t.Fatalf("parse chunk duration %q: %v", out, err)
	}
	if math.Abs(dur-6) > 0.5 {
		t.Fatalf("chunk duration %.3f, want ~6s", dur)
	}
}
