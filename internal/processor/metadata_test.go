package processor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMetadataFromProbe_Tags(t *testing.T) {
	ff := &ffprobeOutput{
		Format: ffprobeFormat{
			Duration: "3.040000",
			Size:     "1048576",
			Tags: map[string]string{
				"creation_time":                 "2024-02-29T12:34:56.000000Z",
				formatTagLocationISO:            "+55.7558+037.6176/",
				formatTagMake:                   "Apple",
				formatTagModel:                  "iPhone 13 Pro Max",
				"com.apple.quicktime.encoded_by": "foo",
			},
		},
	}

	m := metadataFromProbe(ff)

	if m.Duration != 3.04 {
		t.Errorf("duration = %v, want 3.04", m.Duration)
	}
	if m.Size != 1048576 {
		t.Errorf("size = %v, want 1048576", m.Size)
	}
	wantDate := time.Date(2024, 2, 29, 12, 34, 56, 0, time.UTC)
	if !m.RecordedAt.Equal(wantDate) {
		t.Errorf("recorded_at = %v, want %v", m.RecordedAt, wantDate)
	}
	if m.Location != "55.75580,37.61760" {
		t.Errorf("location = %q", m.Location)
	}
	if m.Camera != "Apple iPhone 13 Pro Max" {
		t.Errorf("camera = %q", m.Camera)
	}
	if got := m.RecordedAtString(); got != "2024-02-29T12:34:56Z" {
		t.Errorf("recorded_at string = %q", got)
	}
}

func TestMetadataFromProbe_QuickTimeCreationdate(t *testing.T) {
	ff := &ffprobeOutput{
		Format: ffprobeFormat{
			Tags: map[string]string{
				formatTagQuickTimeDate: "2023-05-01T12:34:56+0300",
			},
		},
	}

	m := metadataFromProbe(ff)

	want := time.Date(2023, 5, 1, 9, 34, 56, 0, time.UTC)
	if !m.RecordedAt.Equal(want) {
		t.Errorf("recorded_at = %v, want %v", m.RecordedAt, want)
	}
	if m.RecordedAtString() != "2023-05-01T09:34:56Z" {
		t.Errorf("recorded_at string = %q", m.RecordedAtString())
	}
}

func TestMetadataFromProbe_StreamTagFallback(t *testing.T) {
	ff := &ffprobeOutput{
		Format:  ffprobeFormat{Tags: map[string]string{}},
		Streams: []ffprobeStream{{Tags: map[string]string{"creation_time": "2020-01-02T03:04:05.000000Z"}}},
	}

	m := metadataFromProbe(ff)

	want := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if !m.RecordedAt.Equal(want) {
		t.Errorf("recorded_at = %v, want %v", m.RecordedAt, want)
	}
}

func TestMetadataFromProbe_PlainLocationISO6709(t *testing.T) {
	ff := &ffprobeOutput{
		Format: ffprobeFormat{
			Tags: map[string]string{
				formatTagLocation: "+55.0090+073.2981/",
			},
		},
	}

	m := metadataFromProbe(ff)

	if m.Location != "55.00900,73.29810" {
		t.Errorf("location = %q", m.Location)
	}
}

func TestMetadataFromProbe_StreamPlainLocationISO6709(t *testing.T) {
	ff := &ffprobeOutput{
		Format:  ffprobeFormat{Tags: map[string]string{}},
		Streams: []ffprobeStream{{Tags: map[string]string{formatTagLocationPlain: "+40.7128-074.0060/"}}},
	}

	m := metadataFromProbe(ff)

	if m.Location != "40.71280,-74.00600" {
		t.Errorf("location = %q", m.Location)
	}
}

func TestMetadataFromProbe_HumanLocation(t *testing.T) {
	ff := &ffprobeOutput{
		Format: ffprobeFormat{
			Tags: map[string]string{
				formatTagLocation: "Somewhere, Else",
			},
		},
	}

	m := metadataFromProbe(ff)

	if m.Location != "Somewhere, Else" {
		t.Errorf("location = %q", m.Location)
	}
}

func TestMetadataFromProbe_StreamTagFallbackLocationCamera(t *testing.T) {
	ff := &ffprobeOutput{
		Format: ffprobeFormat{Tags: map[string]string{}},
		Streams: []ffprobeStream{{Tags: map[string]string{
			formatTagLocationISO: "+40.7128-074.0060/",
			formatTagMake:        "GoPro",
			formatTagModel:       "HERO10 Black",
		}}},
	}

	m := metadataFromProbe(ff)

	if m.Location != "40.71280,-74.00600" {
		t.Errorf("location = %q", m.Location)
	}
	if m.Camera != "GoPro HERO10 Black" {
		t.Errorf("camera = %q", m.Camera)
	}
}

func TestMetadataFromProbe_PhoneWithoutLocation(t *testing.T) {
	ff := &ffprobeOutput{
		Format: ffprobeFormat{
			Tags: map[string]string{
				"creation_time": "garbage that does not parse",
			},
		},
	}

	m := metadataFromProbe(ff)

	if !m.RecordedAt.IsZero() {
		t.Errorf("recorded_at = %v, want zero", m.RecordedAt)
	}
	if m.Location != "" {
		t.Errorf("location = %q, want empty", m.Location)
	}
	if m.Camera != "" {
		t.Errorf("camera = %q, want empty", m.Camera)
	}
}

func TestParseISO6709_DecimalDegrees(t *testing.T) {
	cases := []struct {
		in       string
		wantLat  float64
		wantLng  float64
		wantOK   bool
	}{
		{"+55.7558+037.6176/", 55.7558, 37.6176, true},
		{"+52.1816+005.0215/", 52.1816, 5.0215, true},
		{"-33.8568+151.2153/", -33.8568, 151.2153, true},
		{"+55.7558+037.6176+161.000/", 55.7558, 37.6176, true},
		{"", 0, 0, false},
		{"55.7558+037.6176/", 0, 0, false},
		{"+55.7558/", 0, 0, false},
	}
	for _, c := range cases {
		lat, lng, ok := parseISO6709(c.in)
		if ok != c.wantOK {
			t.Errorf("parseISO6709(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if lat != c.wantLat || lng != c.wantLng {
			t.Errorf("parseISO6709(%q) = (%.6f,%.6f), want (%.6f,%.6f)",
				c.in, lat, lng, c.wantLat, c.wantLng)
		}
	}
}

func TestParseISO6709Axis_FixedWidth(t *testing.T) {
	// 43 degrees, 30 minutes, 37 seconds
	val, rest, ok := parseISO6709Axis("+433037")
	if !ok {
		t.Fatalf("parse failed")
	}
	want := 43.0 + 30.0/60 + 37.0/3600
	if val != want {
		t.Errorf("val = %v, want %v", val, want)
	}
	if rest != "" {
		t.Errorf("rest = %q", rest)
	}
}

func TestParseISO6709Axis_Remainder(t *testing.T) {
	val, rest, ok := parseISO6709Axis("+55.7558+037.6176/")
	if !ok {
		t.Fatalf("parse failed")
	}
	if val != 55.7558 {
		t.Errorf("val = %v", val)
	}
	if rest != "+037.6176/" {
		t.Errorf("rest = %q", rest)
	}
}

func TestRecordedAtString_Zero(t *testing.T) {
	if got := (VideoMetadata{}).RecordedAtString(); got != "" {
		t.Errorf("zero value recorded_at string = %q, want empty", got)
	}
}

func TestProbeMetadata_InvalidInput(t *testing.T) {
	p := newTestProcessor(EncoderCPU)
	meta, err := p.ProbeMetadata(context.Background(), "/nonexistent/file.mp4")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if meta.Duration != 0 {
		t.Errorf("duration = %v", meta.Duration)
	}
}

func TestProbeMetadata_RealFile(t *testing.T) {
	ffmpeg := "ffmpeg"
	if _, err := exec.LookPath(ffmpeg); err != nil {
		t.Skip("ffmpeg not available")
	}

	dir := t.TempDir()
	clip := filepath.Join(dir, "input.mp4")
	args := []string{
		"-y", "-f", "lavfi",
		"-i", "testsrc=duration=1:size=160x120:rate=10",
		"-c:v", "libx264", "-preset", "ultrafast", "-threads", "1",
		"-movflags", "use_metadata_tags",
		"-metadata", "creation_time=2021-06-15T08:30:00.000000Z",
		"-metadata", "com.apple.quicktime.location.ISO6709=+40.7128-074.0060/",
		"-metadata", "com.apple.quicktime.make=PixelCam",
		"-metadata", "com.apple.quicktime.model=XC-42",
		clip,
	}
	cmd := exec.Command(ffmpeg, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg clip generation failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	p := newTestProcessor(EncoderCPU)
	meta, err := p.ProbeMetadata(context.Background(), clip)
	if err != nil {
		t.Fatalf("ProbeMetadata: %v", err)
	}

	if meta.Duration < 0.5 || meta.Duration > 3 {
		t.Errorf("duration = %v, want ~1s", meta.Duration)
	}
	if meta.Size <= 0 {
		t.Errorf("size = %v, want > 0", meta.Size)
	}
	want := time.Date(2021, 6, 15, 8, 30, 0, 0, time.UTC)
	if !meta.RecordedAt.Equal(want) {
		t.Errorf("recorded_at = %v, want %v (from tag %q)", meta.RecordedAt, want, meta.RecordedAtString())
	}
	if meta.Location != "40.71280,-74.00600" {
		t.Errorf("location = %q", meta.Location)
	}
	if meta.Camera != "PixelCam XC-42" {
		t.Errorf("camera = %q", meta.Camera)
	}
}

func TestProbeMetadata_TmpPathUsesFFmpegDir(t *testing.T) {
	p := newTestProcessor(EncoderCPU)
	probe := p.probePath()
	if probe == "ffprobe" {
		t.Skip("ffprobe not resolvable from bin dir on this machine")
	}
	if _, err := os.Stat(probe); err != nil {
		t.Errorf("probePath %q: %v", probe, err)
	}
}