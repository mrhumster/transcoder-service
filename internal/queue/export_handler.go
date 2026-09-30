package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	pb "github.com/mrhumster/transcoder-service/gen/go/stream"
	"github.com/mrhumster/transcoder-service/internal/metrics"
	"github.com/mrhumster/transcoder-service/internal/processor"
	"github.com/mrhumster/transcoder-service/internal/storage"
)

// exportMaxErrorLen bounds the message reported back to stream-service, which
// stores it on the row and returns it to the browser.
const exportMaxErrorLen = 500

// HandleVideoExport builds the cached mp4 for one stream and reports the
// outcome to stream-service, which owns the state row.
type HandleVideoExport struct {
	processor     processor.Muxer
	storage       storage.FileStorage
	streamService pb.StreamServiceClient
	// notify, when set, announces a finished export (used for the email).
	notify func(ctx context.Context, p VideoExportPayload, size int64) error
}

func NewHandleVideoExport(p processor.Muxer, s storage.FileStorage, svc pb.StreamServiceClient, notify func(context.Context, VideoExportPayload, int64) error) *HandleVideoExport {
	return &HandleVideoExport{processor: p, storage: s, streamService: svc, notify: notify}
}

func (h *HandleVideoExport) HandleVideoExportTask(ctx context.Context, t *asynq.Task) error {
	var p VideoExportPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		// A payload we cannot parse will never parse; retrying is pointless and
		// there is no stream id to report the failure against either.
		return fmt.Errorf("json unmarshal failed: %v: %w", err, asynq.SkipRetry)
	}
	if p.StreamUUID == uuid.Nil {
		return fmt.Errorf("stream uuid is required: %w", asynq.SkipRetry)
	}

	start := time.Now()
	err := h.export(ctx, p)
	if err != nil {
		metrics.Errors.Inc()
		// The row must always leave pending, otherwise the UI waits forever and
		// the object can never be retried without a manual reset.
		if rerr := h.report(ctx, p, 0, err); rerr != nil {
			slog.Error("failed to report export failure", "stream", p.StreamUUID, "error", rerr)
		}
		return err
	}

	metrics.Processed.Inc()
	metrics.Duration.Observe(time.Since(start).Seconds())
	return nil
}

func (h *HandleVideoExport) export(ctx context.Context, p VideoExportPayload) error {
	started := time.Now()
	workDir := filepath.Join(os.TempDir(), "export-"+p.StreamUUID.String())
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	defer func() {
		if rerr := os.RemoveAll(workDir); rerr != nil {
			slog.Warn("failed to clean export work dir", "dir", workDir, "error", rerr)
		}
	}()

	remoteDir := fmt.Sprintf("processed/%s", p.StreamUUID)
	playlist := filepath.Join(workDir, "index.m3u8")
	output := filepath.Join(workDir, "video.mp4")

	slog.Info("downloading HLS rendition", "stream", p.StreamUUID, "remote", remoteDir)
	if err := h.storage.DownloadDir(ctx, remoteDir, workDir); err != nil {
		if isMissing(err) {
			// The rendition is gone (reprocessed into a different prefix, or the
			// bucket was pruned). Retrying will not bring it back.
			return fmt.Errorf("hls rendition missing: %w: %w", err, asynq.SkipRetry)
		}
		if isDiskFull(err) {
			metrics.DiskFull.Inc()
			return fmt.Errorf("disk full: %w: %w", err, asynq.SkipRetry)
		}
		return fmt.Errorf("download rendition: %w", err)
	}
	if _, err := os.Stat(playlist); err != nil {
		return fmt.Errorf("playlist %s missing from the rendition: %w: %w", playlist, err, asynq.SkipRetry)
	}

	if err := h.processor.MuxToMP4(ctx, playlist, output); err != nil {
		if isDiskFull(err) {
			metrics.DiskFull.Inc()
			return fmt.Errorf("disk full: %w: %w", err, asynq.SkipRetry)
		}
		return err
	}

	stat, err := os.Stat(output)
	if err != nil {
		return fmt.Errorf("stat muxed file: %w", err)
	}
	if stat.Size() == 0 {
		return fmt.Errorf("mux produced an empty file: %w", asynq.SkipRetry)
	}

	remoteKey := fmt.Sprintf("processed/%s/video.mp4", p.StreamUUID)
	slog.Info("uploading mp4", "stream", p.StreamUUID, "remote", remoteKey, "size", stat.Size())
	if err := h.storage.Upload(ctx, remoteKey, output, "video/mp4"); err != nil {
		if isDiskFull(err) {
			metrics.DiskFull.Inc()
			return fmt.Errorf("disk full: %w: %w", err, asynq.SkipRetry)
		}
		return fmt.Errorf("upload mp4: %w", err)
	}

	if err := h.report(ctx, p, stat.Size(), nil); err != nil {
		return fmt.Errorf("report export ready: %w", err)
	}

	// Notification last: the file is already downloadable, so a mailer outage
	// must not fail the export or leave the row pending.
	if h.notify != nil {
		if err := h.notify(ctx, p, stat.Size()); err != nil {
			slog.Warn("export ready notification failed", "stream", p.StreamUUID, "error", err)
		}
	}

	slog.Info("export completed", "stream", p.StreamUUID, "size", stat.Size(), "took", time.Since(started))
	return nil
}

// report tells stream-service the outcome. A failed export is a business
// outcome, not an rpc error, so the message travels in the request.
func (h *HandleVideoExport) report(ctx context.Context, p VideoExportPayload, size int64, cause error) error {
	req := &pb.CompleteStreamExportRequest{
		StreamUuid: p.StreamUUID.String(),
		Success:    cause == nil,
		Size:       size,
	}
	if cause != nil {
		req.Error = truncate(cause.Error(), exportMaxErrorLen)
	}
	_, err := h.streamService.CompleteStreamExport(ctx, req)
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func isMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "does not exist")
}

func isDiskFull(err error) bool {
	return strings.Contains(err.Error(), "no space left on device")
}
