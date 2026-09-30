package processor

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

//go:generate mockgen -source=mux.go -destination=mock/muxer_mock.go -package=mock

// Muxer concatenates an already-transcoded HLS rendition into a single mp4.
type Muxer interface {
	MuxToMP4(ctx context.Context, playlistPath, outputPath string) error
}

// MuxToMP4 remuxes, never re-encodes.
//
// The HLS segments are already H.264 + AAC, so `-c copy` makes this a metadata
// rewrite: no quality loss, and roughly a hundred times faster than a re-encode,
// which is what keeps an export of a long video cheap. Two details are not
// optional here:
//
//   - aac_adtstoasc: AAC in MPEG-TS carries AudioSpecificConfig inside every
//     packet, while MP4 expects it once in the extradata. Without the bitstream
//     filter Chrome refuses to play the audio.
//   - +faststart: without it the moov atom lands at the end of the file, so a
//     player cannot start until the whole download finishes, and the browser
//     file picker gets a file that appears to stall.
func (p *FFmpegProcessor) MuxToMP4(ctx context.Context, playlistPath, outputPath string) error {
	args := []string{
		"-hide_banner",
		"-nostdin",
		// The TS segments can carry non-monotonic timestamps after a seek or a
		// discontinuity; regenerate them so the mp4 does not end up unseekable.
		"-fflags", "+genpts",
		"-i", playlistPath,
		"-map", "0:v:0",
		"-map", "0:a:0?",
		"-c", "copy",
		"-bsf:a", "aac_adtstoasc",
		"-movflags", "+faststart",
		"-f", "mp4",
		"-y",
		outputPath,
	}

	cmd := exec.CommandContext(ctx, p.binPath, args...)
	// ffmpeg logs to stderr; capture it so a failure explains itself instead of
	// failing with a bare exit status.
	var stderr strings.Builder
	cmd.Stderr = &stderr

	slog.Info("starting ffmpeg mux", "playlist", playlistPath, "output", outputPath)
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 2000 {
			msg = msg[len(msg)-2000:]
		}
		return fmt.Errorf("ffmpeg mux failed: %w: %s", err, msg)
	}
	return nil
}
