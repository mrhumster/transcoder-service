package main

import (
	"log/slog"
	"os"

	"github.com/hibiken/asynq"
	"github.com/mrhumster/transcoder-service/config"
	"github.com/mrhumster/transcoder-service/internal/queue"
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
		return
	}
	r := asynq.RedisClientOpt{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Passwrod,
		DB:       2,
	}
	srv := asynq.NewServer(r, asynq.Config{Concurrency: 3})

	mux := asynq.NewServeMux()

	mux.HandleFunc(queue.TaskVideoTranscoding, queue.HandleVideoTranscodeTask)

	slog.Info("Transoder Worker started...")
	if err := srv.Run(mux); err != nil {
		slog.Error("could not run asynq server:", "error", err.Error())
		return
	}
}
