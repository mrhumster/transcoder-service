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

	"github.com/hibiken/asynq"
	pb "github.com/mrhumster/transcoder-service/gen/go/stream"
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

	slog.Info("downloading source", "path", p.InputPath)
	if err := h.storage.Download(ctx, p.InputPath, inputLocal); err != nil {
		slog.Error("downaload failed",
			"uuid", p.StreamUUID,
			"inputPath", p.InputPath,
			"inputLocal", inputLocal,
			"error", err)

		if strings.Contains(err.Error(), "does not exist") {
			return fmt.Errorf("source missing: %w", asynq.SkipRetry)
		}

		if strings.Contains(err.Error(), "no space left on device") {
			return fmt.Errorf("disk full: %w", asynq.SkipRetry)
		}

		return nil
	}

	duration, err := h.processor.GetDuration(ctx, inputLocal)
	if err != nil {
		slog.Error("failed to get duration", "error", err)
		duration = 0
	}
	slog.Info("getting duration", "value", duration)

	_, err = h.streamService.UpdateStreamMetadata(ctx, &pb.UpdateStreamMetadataRequest{
		StreamUuid: p.StreamUUID.String(),
		Duration:   int32(duration),
		Format:     "hls",
		Resolution: "1280x720",
	})
	if err != nil {
		slog.Error("update metadata failed", "error", err)
		return err
	}

	slog.Info("processing", "uuid", p.StreamUUID)

	progChan, errChan := h.processor.TranscodeToHLS(ctx, inputLocal, hlsOutputDir)

	var lastSentPercent int32 = -1
	const limit = 9 * 1024 * 1024 * 1024

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
					Error:      "Not enough disk space on worker",
				})
				return fmt.Errorf("no space left: %w", asynq.SkipRetry)
			}

			currentPercent := int32(prog.Percent)
			if currentPercent >= lastSentPercent+5 || currentPercent >= 100 {
				updateProgReq := &pb.UpdateStreamProcessingRequest{
					StreamUuid: p.StreamUUID.String(),
					Progress:   int32(prog.Percent),
					Steps:      []string{"Transcoding"},
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
					Error:      fmt.Sprintf("failed convert: %s", err.Error()),
				})
				slog.Error("FFMPEG ERROR", "err", err)
				if strings.Contains(err.Error(), "no space left on device") {
					return fmt.Errorf("disk full: %w", asynq.SkipRetry)
				}
				return err
			}
		case <-ctx.Done():
			slog.Warn("Context cancelled, stopping...")
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
	}
	_, err = h.streamService.UpdateStreamProcessing(ctx, updateProgReq)
	if err != nil {
		slog.Error("failed send progress in stream service", "error", err)
	}

	return nil
}
