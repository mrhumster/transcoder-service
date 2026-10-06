package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
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

type HandleVideoTrancoder struct {
	processor     processor.VideoProcessor // convert video
	storage       storage.FileStorage      // save to cloud
	streamService pb.StreamServiceClient   // update in db
}

func NewHandleVideoTranscoder(p processor.VideoProcessor, s storage.FileStorage, svc pb.StreamServiceClient) *HandleVideoTrancoder {
	return &HandleVideoTrancoder{
		processor:     p,
		storage:       s,
		streamService: svc,
	}
}

func getDirSize(path string) uint64 {
	var size int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	if err != nil {
		return 0
	}
	return uint64(size)
}

func (h *HandleVideoTrancoder) HandleVideoTranscoderTask(ctx context.Context, t *asynq.Task) error {
	var p VideoTranscodingPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("json unmarshal failed: %v", err)
	}

	start := time.Now()
	taskErr := h.handleTranscode(ctx, p)
	if taskErr != nil {
		metrics.Errors.Inc()
	} else {
		metrics.Processed.Inc()
		metrics.Duration.Observe(time.Since(start).Seconds())
	}
	return taskErr
}

// presignTTL must outlast the longest transcode task so ffmpeg can keep
// re-seeking on the url for the entire source playback.
const presignTTL = 24 * time.Hour

// resolveSource picks the input for ffprobe/ffmpeg and returns it together
// with the probed metadata. The preferred path is a presigned url: ffmpeg
// then reads the object straight from the storage with Range seeks, so a
// multi-gigabyte source never lands on the worker disk (previously the whole
// file was downloaded, which capped usable inputs at ~1.8 GB of page cache
// and the 10 Gi emptyDir). When presigning or probing over http fails, it
// falls back to downloading the object locally — the pre-url behaviour.
// A download failure maps onto asynq.SkipRetry for terminal cases.
func (h *HandleVideoTrancoder) resolveSource(ctx context.Context, p VideoTranscodingPayload, inputLocal string) (string, processor.VideoMetadata, error) {
	inputURL, presignErr := h.storage.GeneratePresignedURL(ctx, p.InputPath, presignTTL)
	if presignErr != nil {
		slog.Warn("presign failed, falling back to local download",
			"uuid", p.StreamUUID, "path", p.InputPath, "error", presignErr)
	} else {
		meta, probeErr := h.processor.ProbeMetadata(ctx, inputURL)
		if probeErr == nil {
			if size, statErr := h.storage.StatSize(ctx, p.InputPath); statErr == nil {
				meta.Size = size
			} else {
				slog.Warn("stat source failed, keeping probed size",
					"uuid", p.StreamUUID, "path", p.InputPath, "error", statErr)
			}
			logProbedMetadata(p.StreamUUID, meta)
			return inputURL, meta, nil
		}
		slog.Warn("probe over presigned url failed, falling back to local download",
			"uuid", p.StreamUUID, "path", p.InputPath, "error", probeErr)
	}

	slog.Info("downloading source", "path", p.InputPath)
	if err := h.storage.Download(ctx, p.InputPath, inputLocal); err != nil {
		slog.Error("download failed",
			"uuid", p.StreamUUID,
			"inputPath", p.InputPath,
			"inputLocal", inputLocal,
			"error", err)

		if strings.Contains(err.Error(), "does not exist") {
			return "", processor.VideoMetadata{}, fmt.Errorf("source missing: %w", asynq.SkipRetry)
		}
		if strings.Contains(err.Error(), "no space left on device") {
			metrics.DiskFull.Inc()
			return "", processor.VideoMetadata{}, fmt.Errorf("disk full: %w", asynq.SkipRetry)
		}
		return "", processor.VideoMetadata{}, fmt.Errorf("download failed: %w", err)
	}

	// A probe failure here is non-terminal (kept as a log line): the stream
	// still transcodes, it just publishes empty metadata fields.
	meta, err := h.processor.ProbeMetadata(ctx, inputLocal)
	if err != nil {
		slog.Error("metadata probe failed", "error", err)
	}
	if stat, statErr := os.Stat(inputLocal); statErr == nil {
		meta.Size = stat.Size()
	}
	logProbedMetadata(p.StreamUUID, meta)
	return inputLocal, meta, nil
}

func logProbedMetadata(id uuid.UUID, meta processor.VideoMetadata) {
	slog.Info("probed source metadata",
		"uuid", id,
		"duration", meta.Duration,
		"size", meta.Size,
		"recorded_at", meta.RecordedAtString(),
		"location", meta.Location,
		"camera", meta.Camera,
	)
}

func (h *HandleVideoTrancoder) handleTranscode(ctx context.Context, p VideoTranscodingPayload) error {

	workDir := fmt.Sprintf("/tmp/%s", p.StreamUUID)
	inputLocal := workDir + "/input.mp4"
	hlsOutputDir := workDir + "/hls"
	outputDir := fmt.Sprintf("/tmp/%s", p.StreamUUID)

	if err := os.Mkdir(outputDir, 0o755); err != nil {
		slog.Error("error creating temp dir", "error", err)
		return err
	}

	if err := os.Mkdir(hlsOutputDir, 0o755); err != nil {
		slog.Error("error creating temp dir", "error", err)
		return err
	}

	defer os.RemoveAll(outputDir)

	inputSrc, meta, err := h.resolveSource(ctx, p, inputLocal)
	if err != nil {
		return err
	}

	_, err = h.streamService.UpdateStreamMetadata(ctx, &pb.UpdateStreamMetadataRequest{
		StreamUuid: p.StreamUUID.String(),
		Duration:   int32(meta.Duration),
		Size:       meta.Size,
		Format:     "hls",
		Resolution: "1280x720",
		RecordedAt: meta.RecordedAtString(),
		Location:   meta.Location,
		Camera:     meta.Camera,
	})
	if err != nil {
		slog.Error("update metadata failed", "error", err)
		return err
	}

	slog.Info("processing", "uuid", p.StreamUUID)

	progChan, errChan := h.processor.TranscodeToHLS(ctx, inputSrc, hlsOutputDir)

	var lastSentPercent int32 = -1
	// /tmp holds only the HLS output when the source is read via presigned
	// url (~1.1 GiB per hour of video); the cap exists for the full-download
	// fallback, where the input plus output share the volume.
	const limit = 12 * 1024 * 1024 * 1024

	for {
		select {
		case prog, ok := <-progChan:
			if !ok {
				slog.Info("channel is close", "progress", prog)
				progChan = nil
				continue
			}
			dirSize := getDirSize("/tmp")
			if dirSize > limit {
				slog.Error("CRITICAL: Out of free space, aborting", "size_tmp", dirSize)
				h.streamService.UpdateStreamProcessing(ctx, &pb.UpdateStreamProcessingRequest{
					StreamUuid: p.StreamUUID.String(),
					Progress:   0,
					Steps:      []string{"Transcoding"},
					Task:       "transcode",
					Error:      "Not enough disk space on worker",
				})
				metrics.DiskFull.Inc()
				return fmt.Errorf("no space left: %w", asynq.SkipRetry)
			}

			currentPercent := int32(prog.Percent)
			if currentPercent >= lastSentPercent+5 || currentPercent >= 100 {
				updateProgReq := &pb.UpdateStreamProcessingRequest{
					StreamUuid: p.StreamUUID.String(),
					Progress:   int32(prog.Percent),
					Steps:      []string{"Transcoding"},
					Task:       "transcode",
				}
				_, err := h.streamService.UpdateStreamProcessing(ctx, updateProgReq)

				if err != nil {
					slog.Error("failed send progress in stream service", "error", err)
				} else {
					lastSentPercent = currentPercent
					slog.Info("send progress updated", "percent", currentPercent)
				}
			}
		case err, ok := <-errChan:
			if !ok {
				if progChan == nil {
					goto upload
				}
				continue
			}
			if err != nil {
				h.streamService.UpdateStreamProcessing(ctx, &pb.UpdateStreamProcessingRequest{
					StreamUuid: p.StreamUUID.String(),
					Progress:   int32(lastSentPercent),
					Steps:      []string{"Transcoding"},
					Task:       "transcode",
					Error:      fmt.Sprintf("failed convert: %s", err.Error()),
				})
				slog.Error("FFMPEG ERROR", "err", err)
				if strings.Contains(err.Error(), "no space left on device") {
					return fmt.Errorf("disk full: %w", asynq.SkipRetry)
				}
				return err
			}
		case <-ctx.Done():
			metrics.Aborted.Inc()
			slog.Warn("Context cancelled, stopping...")
			// On the last attempt the failure is terminal (asynq will archive the
			// task), so report it — otherwise the DB keeps a frozen progress with
			// no error and the stream looks stuck. Earlier attempts are retried
			// silently: a mid-retry error would flip the stream to StatusError.
			if retryCount, ok := asynq.GetRetryCount(ctx); ok {
				if maxRetry, mok := asynq.GetMaxRetry(ctx); mok && retryCount >= maxRetry {
					reportProgress := lastSentPercent
					if reportProgress < 0 {
						reportProgress = 0
					}
					reportCtx, cancelReport := context.WithTimeout(context.Background(), 15*time.Second)
					_, err := h.streamService.UpdateStreamProcessing(reportCtx, &pb.UpdateStreamProcessingRequest{
						StreamUuid: p.StreamUUID.String(),
						Progress:   reportProgress,
						Steps:      []string{"Transcoding"},
						Task:       "transcode",
						Error:      fmt.Sprintf("transcode interrupted: %s", ctx.Err().Error()),
					})
					cancelReport()
					if err != nil {
						slog.Error("failed to report cancelled transcode", "error", err)
					}
				}
			}
			return ctx.Err()
		}

		if progChan == nil && errChan == nil {
			break
		}
	}
upload:
	slog.Info("Starting upload phase", "uuid", p.StreamUUID)

	updateProgReq := &pb.UpdateStreamProcessingRequest{
		StreamUuid: p.StreamUUID.String(),
		Progress:   int32(100),
		Steps:      []string{"Uploading to the storage"},
		Task:       "transcode",
	}
	_, err = h.streamService.UpdateStreamProcessing(ctx, updateProgReq)
	if err != nil {
		slog.Error("failed send progress in stream service", "error", err)
	}

	remoteProcessedDir := fmt.Sprintf("processed/%s", p.StreamUUID)
	slog.Info("uploading HLS result", "remote", remoteProcessedDir)
	if err := h.storage.UploadDir(ctx, remoteProcessedDir, hlsOutputDir); err != nil {
		slog.Error("uploading error",
			"uuid", p.StreamUUID,
			"error", err)
		return err
	}
	slog.Info("transcoding success", "uuid", p.StreamUUID)

	_, err = h.streamService.UpdateStreamStatus(ctx, &pb.UpdateStreamStatusRequest{
		StreamUuid: p.StreamUUID.String(),
		Status:     pb.Status_STATUS_READY,
	})
	if err != nil {
		slog.Error("grpc notify failed", "error", err)
		return err
	}

	updateProgReq = &pb.UpdateStreamProcessingRequest{
		StreamUuid: p.StreamUUID.String(),
		Progress:   int32(100),
		Steps:      []string{},
		Task:       "transcode",
	}
	_, err = h.streamService.UpdateStreamProcessing(ctx, updateProgReq)
	if err != nil {
		slog.Error("failed send progress in stream service", "error", err)
	}

	return nil
}
