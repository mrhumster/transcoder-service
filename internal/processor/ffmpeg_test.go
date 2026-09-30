package processor

import (
	"os"
	"strings"
	"testing"
)

func newTestProcessor(encoder string) *FFmpegProcessor {
	return &FFmpegProcessor{binPath: "/usr/bin/ffmpeg", encoder: encoder}
}

func TestBuildArgs_CPU(t *testing.T) {
	p := newTestProcessor(EncoderCPU)
	args := p.buildArgs("/in/video.mp4", "/out/dir")

	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-threads", "0",
		"-c:v", "libx264",
		"-c:a", "aac",
		"-b:v", "2500k",
		"-maxrate", "2500k",
		"-bufsize", "5000k",
		"-hls_time", "10",
		"-hls_list_size", "0",
		"-f", "hls",
	} {
		if !containsArg(args, want) {
			t.Errorf("cpu args missing %q", want)
		}
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
	args := p.buildArgs("/in/video.mp4", "/out/dir")

	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-vaapi_device", vaapiDevicePath,
		"-c:v", "h264_vaapi",
		"-vf", "format=nv12,hwupload",
		"-c:a", "aac",
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
