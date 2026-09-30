package processor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newTestMuxer mirrors newTestProcessor but points at whatever ffmpeg is on
// PATH, since MuxToMP4 resolves nothing itself.
func newTestMuxer(t *testing.T) *FFmpegProcessor {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	return &FFmpegProcessor{binPath: path, encoder: EncoderCPU}
}

func TestMuxToMP4(t *testing.T) {
	p := newTestMuxer(t)

	dir := t.TempDir()
	// One real H.264 + AAC rendition, muxed the same way the transcoder would.
	src := filepath.Join(dir, "source.mp4")
	run(t, p.binPath,
		"-v", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=15:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-g", "15",
		"-c:a", "aac", "-b:a", "64k",
		"-shortest",
		src,
	)

	hlsDir := filepath.Join(dir, "hls")
	if err := os.MkdirAll(hlsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, p.binPath,
		"-v", "error",
		"-i", src,
		"-c", "copy",
		"-f", "hls", "-hls_time", "1", "-hls_list_size", "0",
		"-hls_segment_filename", filepath.Join(hlsDir, "seg_%d.ts"),
		filepath.Join(hlsDir, "index.m3u8"),
	)

	out := filepath.Join(dir, "video.mp4")
	if err := p.MuxToMP4(context.Background(), filepath.Join(hlsDir, "index.m3u8"), out); err != nil {
		t.Fatalf("MuxToMP4: %v", err)
	}

	stat, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if stat.Size() == 0 {
		t.Fatal("muxed file is empty")
	}

	// A plain header copy would leave the audio unplayable in a browser, so
	// assert the moov atom is at the front (faststart) rather than at the end.
	head, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer head.Close()
	first := make([]byte, 64)
	if _, err := head.Read(first); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "ftyp") {
		t.Fatalf("output does not start with an mp4 ftyp box: %q", first[:16])
	}
}

func TestMuxToMP4ReportsFailure(t *testing.T) {
	p := newTestMuxer(t)
	dir := t.TempDir()

	// An empty playlist is a valid input that yields no streams, so ffmpeg
	// exits non-zero and the caller must learn why.
	empty := filepath.Join(dir, "index.m3u8")
	if err := os.WriteFile(empty, []byte("#EXTM3U\n#EXT-X-ENDLIST\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := p.MuxToMP4(context.Background(), empty, filepath.Join(dir, "out.mp4"))
	if err == nil {
		t.Fatal("expected an error for a playlist with no streams")
	}
	// The captured stderr is what makes the failure diagnosable in the row.
	if !strings.Contains(err.Error(), "ffmpeg mux failed") {
		t.Fatalf("error should name the failing step, got: %v", err)
	}
}

func TestMuxToMP4RespectsContextCancellation(t *testing.T) {
	p := newTestMuxer(t)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.MuxToMP4(ctx, filepath.Join(dir, "missing.m3u8"), filepath.Join(dir, "out.mp4"))
	if err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
}

func run(t *testing.T, bin string, args ...string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", bin, strings.Join(args, " "), err, out)
	}
}
