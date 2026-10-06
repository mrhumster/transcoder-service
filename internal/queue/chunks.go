package queue

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	pb "github.com/mrhumster/transcoder-service/gen/go/stream"
	"github.com/mrhumster/transcoder-service/internal/processor"
)

// chunkDurationSec is the target length of one transcode chunk. A longer
// source is split at these boundaries, so a single ffmpeg run never holds
// more than ~30 minutes of output on disk and an interrupted attempt
// resumes from the last completed chunk instead of re-encoding from zero.
// A var, so tests can shrink it.
var chunkDurationSec = 30 * 60.0

// chunkWatchdog cancels a chunk when ffmpeg stops emitting progress: a dead
// presigned url or a wedged encoder would otherwise hold the task until the
// asynq timeout with no signal in the UI. A var for tests.
var chunkWatchdog = 10 * time.Minute

// chunkMarker is the checkpoint uploaded right after a chunk's segments: its
// presence in the storage means the chunk is complete, which is what resume
// relies on (FileStorage has no object listing). The recorded segment list
// also carries the EXTINF timings needed to stitch a retried task's final
// playlist without touching the chunk itself.
type chunkMarker struct {
	Version  int            `json:"version"`
	Segments []chunkSegment `json:"segments"`
}

// chunkSegment is one entry of a chunk's playlist. Extinf holds the duration
// string exactly as ffmpeg wrote it, without the trailing comma or title.
type chunkSegment struct {
	File   string `json:"file"`
	Extinf string `json:"extinf"`
}

// chunkCount returns how many chunks the source splits into (1 = stay on the
// single-pass path).
func chunkCount(durationSec float64) int {
	if durationSec <= 0 || chunkDurationSec <= 0 {
		return 1
	}
	n := int(math.Ceil(durationSec / chunkDurationSec))
	if n < 1 {
		return 1
	}
	return n
}

func chunkStartSec(i int) float64 {
	return float64(i) * chunkDurationSec
}

// chunkLenSec is the encoded window of chunk i; the last chunk absorbs the
// remainder and is never longer than the threshold.
func chunkLenSec(i int, durationSec float64) float64 {
	remain := durationSec - chunkStartSec(i)
	if remain <= 0 {
		return 0
	}
	if remain > chunkDurationSec {
		return chunkDurationSec
	}
	return remain
}

func chunkMarkerKey(id uuid.UUID, i int) string {
	return fmt.Sprintf("processed/%s/chunks/done_%d.json", id, i)
}

// handleTranscodeChunked runs the split pipeline: every chunk is transcoded
// and uploaded on its own, a marker records its completion (the resume
// point), and the final index.m3u8 is stitched from the recorded segment
// lists — so a retried task never re-encodes a chunk that already made it to
// the storage.
func (h *HandleVideoTrancoder) handleTranscodeChunked(ctx context.Context, p VideoTranscodingPayload, inputSrc string, meta processor.VideoMetadata) error {
	totalSec := meta.Duration
	n := chunkCount(totalSec)
	workDir := filepath.Join(fmt.Sprintf("/tmp/%s", p.StreamUUID), "chunks")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		slog.Error("error creating temp dir", "error", err)
		return err
	}
	remoteRoot := fmt.Sprintf("processed/%s", p.StreamUUID)

	entries := make([][]chunkSegment, n)
	for i := 0; i < n; i++ {
		if segs, ok := h.loadChunkMarker(ctx, p.StreamUUID, workDir, i); ok {
			slog.Info("chunk already in storage, skipping",
				"uuid", p.StreamUUID, "chunk", i, "of", n, "segments", len(segs))
			entries[i] = segs
			continue
		}
		segs, err := h.runChunk(
			ctx, p, inputSrc,
			filepath.Join(workDir, fmt.Sprintf("c%d", i)),
			i, chunkStartSec(i), chunkLenSec(i, totalSec), totalSec,
		)
		if err != nil {
			return err
		}
		entries[i] = segs
		slog.Info("chunk uploaded", "uuid", p.StreamUUID, "chunk", i, "of", n, "segments", len(segs))
	}

	h.reportUploading(ctx, p.StreamUUID)

	localIndex := filepath.Join(fmt.Sprintf("/tmp/%s", p.StreamUUID), "index.m3u8")
	if err := os.WriteFile(localIndex, []byte(stitchPlaylists(entries)), 0o644); err != nil {
		return fmt.Errorf("write stitched playlist: %w", err)
	}
	remoteIndex := remoteRoot + "/index.m3u8"
	slog.Info("uploading stitched playlist", "uuid", p.StreamUUID, "remote", remoteIndex)
	if err := h.storage.Upload(ctx, remoteIndex, localIndex, "application/x-mpegURL"); err != nil {
		slog.Error("uploading index error", "uuid", p.StreamUUID, "error", err)
		return err
	}

	slog.Info("transcoding success", "uuid", p.StreamUUID, "chunks", n)
	return h.markTranscodeReady(ctx, p.StreamUUID)
}

// loadChunkMarker reports whether chunk i already completed: the marker is
// Stat'ed first (the common miss costs one request) and only downloaded when
// it exists. A missing, unreadable or empty marker is not an error — it just
// means the chunk has to be transcoded again.
func (h *HandleVideoTrancoder) loadChunkMarker(ctx context.Context, id uuid.UUID, workDir string, i int) ([]chunkSegment, bool) {
	key := chunkMarkerKey(id, i)
	if _, err := h.storage.StatSize(ctx, key); err != nil {
		return nil, false
	}
	local := filepath.Join(workDir, fmt.Sprintf("done_%d.json", i))
	if err := h.storage.Download(ctx, key, local); err != nil {
		slog.Warn("chunk marker download failed, transcoding again",
			"uuid", id, "chunk", i, "error", err)
		return nil, false
	}
	defer func() { _ = os.Remove(local) }()
	data, err := os.ReadFile(local)
	if err != nil {
		slog.Warn("chunk marker read failed, transcoding again",
			"uuid", id, "chunk", i, "error", err)
		return nil, false
	}
	var m chunkMarker
	if err := json.Unmarshal(data, &m); err != nil || len(m.Segments) == 0 {
		slog.Warn("chunk marker corrupt, transcoding again",
			"uuid", id, "chunk", i, "error", err)
		return nil, false
	}
	return m.Segments, true
}

// runChunk transcodes one slice, uploads its segments and only then its
// checkpoint marker (last, so a present marker always implies the segments
// made it too). The chunk's local directory is removed afterwards to keep the
// disk guard happy while the next chunk runs.
func (h *HandleVideoTrancoder) runChunk(ctx context.Context, p VideoTranscodingPayload, inputSrc, chunkDir string, idx int, startSec, durSec, totalSec float64) ([]chunkSegment, error) {
	if err := os.MkdirAll(chunkDir, 0o755); err != nil {
		return nil, fmt.Errorf("create chunk dir: %w", err)
	}
	chunkCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	progChan, errChan := h.processor.TranscodeChunk(chunkCtx, inputSrc, chunkDir, startSec, durSec)

	watchdog := time.NewTimer(chunkWatchdog)
	defer watchdog.Stop()

	var lastSentPercent int32 = -1
	for progChan != nil || errChan != nil {
		select {
		case prog, ok := <-progChan:
			if !ok {
				progChan = nil
				continue
			}
			// Any event proves ffmpeg is alive; restart the grace period.
			if !watchdog.Stop() {
				select {
				case <-watchdog.C:
				default:
				}
			}
			watchdog.Reset(chunkWatchdog)

			if getDirSize("/tmp") > diskLimit {
				return nil, h.abortDiskFull(ctx, p.StreamUUID)
			}

			currentPercent := int32(prog.Percent)
			if currentPercent >= lastSentPercent+5 || currentPercent >= 100 {
				_, err := h.streamService.UpdateStreamProcessing(ctx, &pb.UpdateStreamProcessingRequest{
					StreamUuid: p.StreamUUID.String(),
					Progress:   currentPercent,
					Steps:      []string{"Transcoding"},
					Task:       "transcode",
				})
				if err != nil {
					slog.Error("failed send progress in stream service", "error", err)
				} else {
					lastSentPercent = currentPercent
					slog.Info("send progress updated", "percent", currentPercent, "chunk", idx)
				}
			}
		case err, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			if err != nil {
				slog.Error("FFMPEG ERROR", "err", err, "chunk", idx)
				h.reportConvertFailure(ctx, p.StreamUUID, lastSentPercent, err.Error())
				if strings.Contains(err.Error(), "no space left on device") {
					return nil, fmt.Errorf("disk full: %w", asynq.SkipRetry)
				}
				return nil, err
			}
		case <-watchdog.C:
			msg := fmt.Sprintf("no ffmpeg progress for %s", chunkWatchdog)
			cancel()
			slog.Error("chunk stalled", "chunk", idx, "uuid", p.StreamUUID)
			h.reportConvertFailure(ctx, p.StreamUUID, lastSentPercent, msg)
			return nil, fmt.Errorf("chunk %d stalled: %s", idx, msg)
		case <-ctx.Done():
			slog.Warn("Context cancelled, stopping chunk...", "chunk", idx)
			h.reportInterrupted(ctx, p.StreamUUID, lastSentPercent, ctx.Err())
			return nil, ctx.Err()
		}
	}

	segments, err := parseChunkPlaylist(filepath.Join(chunkDir, "index.m3u8"))
	if err != nil {
		slog.Error("chunk output unreadable", "chunk", idx, "error", err)
		h.reportConvertFailure(ctx, p.StreamUUID, lastSentPercent, err.Error())
		return nil, err
	}

	// The per-chunk playlist is an implementation detail: the rendition's
	// index.m3u8 is stitched from the markers, so do not upload it.
	if err := os.Remove(filepath.Join(chunkDir, "index.m3u8")); err != nil {
		return nil, fmt.Errorf("remove chunk playlist: %w", err)
	}

	remoteChunkDir := fmt.Sprintf("processed/%s/chunks/c%d", p.StreamUUID, idx)
	if err := h.storage.UploadDir(ctx, remoteChunkDir, chunkDir); err != nil {
		slog.Error("uploading chunk error",
			"uuid", p.StreamUUID, "chunk", idx, "error", err)
		return nil, err
	}

	marker := chunkMarker{Version: 1, Segments: segments}
	data, err := json.Marshal(marker)
	if err != nil {
		return nil, fmt.Errorf("marshal chunk marker: %w", err)
	}
	markerLocal := filepath.Join(chunkDir, "marker.json")
	if err := os.WriteFile(markerLocal, data, 0o644); err != nil {
		return nil, fmt.Errorf("write chunk marker: %w", err)
	}
	if err := h.storage.Upload(ctx, chunkMarkerKey(p.StreamUUID, idx), markerLocal, "application/json"); err != nil {
		slog.Error("uploading chunk marker error",
			"uuid", p.StreamUUID, "chunk", idx, "error", err)
		return nil, err
	}

	if err := os.RemoveAll(chunkDir); err != nil {
		slog.Warn("failed to clean chunk dir", "dir", chunkDir, "error", err)
	}
	return segments, nil
}

// parseChunkPlaylist reads one chunk's ffmpeg-written playlist back into an
// ordered segment list. Only #EXTINF durations and file lines matter: the
// chunk-local headers are replaced by the stitched playlist's own.
func parseChunkPlaylist(path string) ([]chunkSegment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open chunk playlist: %w", err)
	}
	defer func() { _ = f.Close() }()

	var segs []chunkSegment
	var pending string
	var havePending bool
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "#EXTINF:"):
			value := strings.TrimPrefix(line, "#EXTINF:")
			if comma := strings.IndexByte(value, ','); comma >= 0 {
				value = value[:comma]
			}
			pending = strings.TrimSpace(value)
			havePending = true
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		default:
			if !havePending {
				return nil, fmt.Errorf("chunk playlist: segment %q without #EXTINF", line)
			}
			segs = append(segs, chunkSegment{File: line, Extinf: pending})
			havePending = false
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read chunk playlist: %w", err)
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("chunk playlist: no segments in %s", path)
	}
	return segs, nil
}

// stitchPlaylists assembles the rendition's index.m3u8 from the per-chunk
// segment lists. Segment URIs are relative to the playlist location
// (processed/<uuid>/), so each chunk's files keep their c<i>/ subdirectory
// and two chunks can never collide on a name. #EXT-X-DISCONTINUITY marks
// every boundary: the chunks do not share a perfectly continuous timestamp
// grid (their encoder base differs by a few frames), and players reset their
// clock on the tag instead of glitching.
func stitchPlaylists(entries [][]chunkSegment) string {
	target := 1
	for _, segs := range entries {
		for _, s := range segs {
			if d, err := strconv.ParseFloat(s.Extinf, 64); err == nil {
				if r := int(math.Ceil(d)); r > target {
					target = r
				}
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n", target)
	for i, segs := range entries {
		if i > 0 {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		for _, s := range segs {
			b.WriteString("#EXTINF:")
			b.WriteString(s.Extinf)
			b.WriteString(",\n")
			fmt.Fprintf(&b, "chunks/c%d/%s\n", i, s.File)
		}
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}
