package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/hibiken/asynq"
	pb "github.com/mrhumster/transcoder-service/gen/go/stream"
	"github.com/mrhumster/transcoder-service/internal/processor"
	"github.com/mrhumster/transcoder-service/internal/storage"
)

type HandleVideoTrancoder struct {
	processor     processor.VideoProcessor
	storage       storage.FileStorage
	streamService pb.StreamServiceClient
}

func NewHandleVideoTranscoder(p processor.VideoProcessor, s storage.FileStorage, svc pb.StreamServiceClient) *HandleVideoTrancoder {
	return &HandleVideoTrancoder{
		processor:     p,
		storage:       s,
		streamService: svc,
	}
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
		return nil
	}

	_, err := h.streamService.UpdateStreamMetadata(ctx, &pb.UpdateStreamMetadataRequest{
		StreamUuid: p.StreamUUID.String(),
		Duration:   120,
		Format:     "hls",
		Resolution: "1280x720",
	})
	if err != nil {
		slog.Error("update metadata failed", "error", err)
		return err
	}

	slog.Info("processing", "uuid", p.StreamUUID)
	if err := h.processor.TranscodeToHLS(ctx, inputLocal, hlsOutputDir); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			slog.Info("transcoding canceled, skipping retry", "uuid", p.StreamUUID)
			return nil
		}
		slog.Error("ffmpeg processed failed",
			"uuid", p.StreamUUID,
			"error", err)
		return err
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
	return nil
}
