package main

import (
	"log/slog"
	"os"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mrhumster/transcoder-service/config"
	pb "github.com/mrhumster/transcoder-service/gen/go/stream"
	"github.com/mrhumster/transcoder-service/internal/processor"
	"github.com/mrhumster/transcoder-service/internal/queue"
	"github.com/mrhumster/transcoder-service/internal/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
	srv := asynq.NewServer(r, asynq.Config{Concurrency: 1, ShutdownTimeout: 50 * time.Minute})
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

	conn, err := grpc.NewClient(cfg.Server.StreamSeviceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		slog.Error("error init gRPC client: %w", "error", err)
		os.Exit(1)
	}

	defer conn.Close()

	streamServiceClient := pb.NewStreamServiceClient(conn)

	hanlder := queue.NewHandleVideoTranscoder(ffmpeg, minioStorage, streamServiceClient)
	mux := asynq.NewServeMux()
	mux.HandleFunc(queue.TaskVideoTranscoding, hanlder.HandleVideoTranscoderTask)

	slog.Info("Transoder Worker started...")
	if err := srv.Run(mux); err != nil {
		slog.Error("could not run asynq server:", "error", err.Error())
		os.Exit(1)
	}
}
