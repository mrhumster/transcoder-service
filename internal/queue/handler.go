package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/hibiken/asynq"
	"github.com/mrhumster/transcoder-service/internal/processor"
	"github.com/mrhumster/transcoder-service/internal/storage"
)

type HandleVideoTrancoder struct {
	processor processor.VideoProcessor
	storage   storage.FileStorage
}

func NewHandleVideoTranscoder(p processor.VideoProcessor, s storage.FileStorage) *HandleVideoTrancoder {
	return &HandleVideoTrancoder{
		processor: p,
		storage:   s,
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

	slog.Info("processing", "uuid", p.StreamUUID)
	if err := h.processor.TranscodeToHLS(ctx, inputLocal, hlsOutputDir); err != nil {
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
	return nil
}
