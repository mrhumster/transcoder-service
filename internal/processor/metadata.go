package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Tag keys that commonly carry media-originated metadata in MP4/MOV/QuickTime
// containers produced by phones and cameras.
const (
	formatTagCreationTime = "creation_time"
	formatTagCreationDate  = "creation_date"
	formatTagQuickTimeDate = "com.apple.quicktime.creationdate"

	formatTagLocationISO   = "com.apple.quicktime.location.ISO6709"
	formatTagLocation      = "com.apple.quicktime.location"
	formatTagLocationPlain = "location"

	formatTagMake  = "com.apple.quicktime.make"
	formatTagModel = "com.apple.quicktime.model"
)

// VideoMetadata holds the media-originated fields extracted from the source
// file. Zero values mean the value is absent in the file.
type VideoMetadata struct {
	Duration   float64
	Size       int64
	RecordedAt time.Time
	Location   string
	Camera     string
}

// RecordedAtString formats RecordedAt as an RFC3339Nano string in UTC, or an
// empty string when no creation time was found in the file.
func (m VideoMetadata) RecordedAtString() string {
	if m.RecordedAt.IsZero() {
		return ""
	}
	return m.RecordedAt.UTC().Format(time.RFC3339Nano)
}

// ffprobeFormat mirrors the relevant subset of `ffprobe -print_format json
// -show_format -show_streams`.
type ffprobeFormat struct {
	Duration string            `json:"duration"`
	Size     string            `json:"size"`
	Tags     map[string]string `json:"tags"`
}

type ffprobeStream struct {
	Tags map[string]string `json:"tags"`
}

type ffprobeOutput struct {
	Format  ffprobeFormat   `json:"format"`
	Streams []ffprobeStream `json:"streams"`
}

// ProbeMetadata inspects the source file with ffprobe and returns the
// media-originated metadata: duration, size and the recorded-at/location/camera
// tags when present in the container.
func (p *FFmpegProcessor) ProbeMetadata(ctx context.Context, inputPath string) (VideoMetadata, error) {
	args := []string{
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		inputPath,
	}
	out, err := exec.CommandContext(ctx, p.probePath(), args...).Output()
	if err != nil {
		return VideoMetadata{}, fmt.Errorf("ffprobe probe failed: %w", err)
	}
	var ff ffprobeOutput
	if err := json.Unmarshal(out, &ff); err != nil {
		return VideoMetadata{}, fmt.Errorf("ffprobe output parse failed: %w", err)
	}
	return metadataFromProbe(&ff), nil
}

// metadataFromProbe maps a parsed ffprobe result onto VideoMetadata. Format
// tags are preferred; the first stream's tags are used as a fallback (some
// muxers write the creation/location/device tags into an mdta stream atom).
func metadataFromProbe(ff *ffprobeOutput) VideoMetadata {
	tags := ff.Format.Tags

	m := VideoMetadata{}
	if ff.Format.Duration != "" {
		if d, err := strconv.ParseFloat(ff.Format.Duration, 64); err == nil {
			m.Duration = d
		}
	}
	if ff.Format.Size != "" {
		if s, err := strconv.ParseInt(ff.Format.Size, 10, 64); err == nil {
			m.Size = s
		}
	}

	streamTags := map[string]string{}
	if len(ff.Streams) > 0 && ff.Streams[0].Tags != nil {
		streamTags = ff.Streams[0].Tags
	}

	if t := firstNonEmpty(
		tags[formatTagCreationTime],
		streamTags[formatTagCreationTime],
		tags[formatTagQuickTimeDate],
		streamTags[formatTagQuickTimeDate],
		tags[formatTagCreationDate],
		streamTags[formatTagCreationDate],
	); t != "" {
		if tm, err := parseRecordedAt(t); err == nil {
			m.RecordedAt = tm
		}
	}

	if iso := firstNonEmpty(tags[formatTagLocationISO], streamTags[formatTagLocationISO]); iso != "" {
		if lat, lng, ok := parseISO6709(iso); ok {
			m.Location = fmt.Sprintf("%.5f,%.5f", lat, lng)
		}
	}
	if m.Location == "" {
		if loc := firstNonEmpty(
			tags[formatTagLocation],
			streamTags[formatTagLocation],
			tags[formatTagLocationPlain],
			streamTags[formatTagLocationPlain],
		); loc != "" {
			m.Location = strings.TrimSpace(loc)
		}
	}

	m.Camera = cameraFromTags(mergeTags(tags, streamTags))
	return m
}

// mergeTags returns a combined tag map, preferring format-level values when a
// key exists in both maps.
func mergeTags(formatTags, streamTags map[string]string) map[string]string {
	merged := map[string]string{}
	for k, v := range streamTags {
		merged[k] = v
	}
	for k, v := range formatTags {
		merged[k] = v
	}
	return merged
}

func cameraFromTags(tags map[string]string) string {
	make_, model := strings.TrimSpace(tags[formatTagMake]), strings.TrimSpace(tags[formatTagModel])
	switch {
	case make_ != "" && model != "":
		return make_ + " " + model
	case make_ != "":
		return make_
	case model != "":
		return model
	default:
		return ""
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

var recordedAtLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05Z0700", // QuickTime: no colon in the offset
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02",
}

func parseRecordedAt(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	for _, layout := range recordedAtLayouts {
		if tm, err := time.Parse(layout, v); err == nil {
			return tm, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable timestamp %q", v)
}

// parseISO6709 parses an ISO 6709 coordinate string as written by cameras
// (e.g. "+55.7558+037.6176+161.000/") into decimal latitude/longitude. A
// non-empty altitude suffix after the longitude is ignored.
func parseISO6709(v string) (lat, lng float64, ok bool) {
	v = strings.TrimSuffix(strings.TrimSpace(v), "/")
	lat, rest, ok1 := parseISO6709Axis(v)
	if !ok1 || rest == "" {
		return 0, 0, false
	}
	lng, _, ok2 := parseISO6709Axis(rest)
	if !ok2 {
		return 0, 0, false
	}
	return lat, lng, true
}

// parseISO6709Axis parses one signed axis ("+55.7558" or "+433037") returning
// the decimal degrees value and the remaining string. Tokens without a decimal
// point are interpreted as fixed-width DDDMMSS packing, with the trailing four
// characters being minutes and seconds when present.
func parseISO6709Axis(s string) (val float64, rest string, ok bool) {
	if s == "" {
		return 0, "", false
	}
	sign := 1.0
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		sign = -1
		s = s[1:]
	default:
		return 0, "", false
	}

	token, rest := takeUntilSign(s)
	if token == "" {
		return 0, "", false
	}

	if strings.Contains(token, ".") {
		num, err := strconv.ParseFloat(token, 64)
		if err != nil {
			return 0, "", false
		}
		return sign * num, rest, true
	}

	var degStr, minStr, secStr string
	switch {
	case len(token) <= 3:
		degStr, minStr, secStr = token, "0", "0"
	case len(token) == 4:
		degStr, minStr, secStr = token[:2], token[2:], "0"
	default:
		degStr = token[:len(token)-4]
		minStr = token[len(token)-4 : len(token)-2]
		secStr = token[len(token)-2:]
	}

	deg, err1 := strconv.ParseFloat(degStr, 64)
	min, err2 := strconv.ParseFloat(minStr, 64)
	sec, err3 := strconv.ParseFloat(secStr, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, "", false
	}
	return sign * (deg + min/60 + sec/3600), rest, true
}

func takeUntilSign(s string) (token, rest string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '+' || s[i] == '-' {
			return s[:i], s[i:]
		}
	}
	return s, ""
}