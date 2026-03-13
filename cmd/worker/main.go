package main

import (
	"log/slog"
	"os"

	"github.com/hibiken/asynq"
	"github.com/mrhumster/transcoder-service/config"
	"github.com/mrhumster/transcoder-service/internal/processor"
	"github.com/mrhumster/transcoder-service/internal/queue"
	"github.com/mrhumster/transcoder-service/internal/storage"
)

func main() {
	opts := &slog.HandlerOptions{
		Level:     slog.LevelDebug,
		AddSource: true,
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, opts))

	slog.SetDefault(logger)

	cfg, err := config.LoadConfig()
	if err != nil {
		slog.Error("error load config")
		os.Exit(1)
	}
	r := asynq.RedisClientOpt{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Passwrod,
		DB:       2,
	}
	srv := asynq.NewServer(r, asynq.Config{Concurrency: 3})
	minioStorage, err := storage.NewMinIOStorageFromConfig(cfg.MinIO)
	if err != nil {
		slog.Error("error init minio storage", "error", err)
		os.Exit(1)
	}
	ffmpeg, err := processor.NewFFmpegProcessor()
	if err != nil {
		slog.Error("error init processor", "error", err)
		os.Exit(1)
	}
	hanlder := queue.NewHandleVideoTranscoder(ffmpeg, minioStorage)
	mux := asynq.NewServeMux()
	mux.HandleFunc(queue.TaskVideoTranscoding, hanlder.HandleVideoTranscoderTask)

	slog.Info("Transoder Worker started...")
	if err := srv.Run(mux); err != nil {
		slog.Error("could not run asynq server:", "error", err.Error())
		os.Exit(1)
	}
}
