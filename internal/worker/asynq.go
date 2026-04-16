package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mrhumster/transcoder-service/config"
	pb "github.com/mrhumster/transcoder-service/gen/go/stream"
	"github.com/mrhumster/transcoder-service/internal/queue"
	"github.com/redis/go-redis/v9"
)

func NewAsynqWorker(c *config.Config, streamSvc pb.StreamServiceClient) *asynq.Server {
	redisClientOpt := asynq.RedisClientOpt{
		Addr:     c.Redis.Addr,
		Password: c.Redis.Passwrod,
		DB:       c.Redis.DB,
	}

	checkClient := redisClientOpt.MakeRedisClient().(redis.UniversalClient)
	if err := checkClient.Ping(context.Background()).Err(); err != nil {
		slog.Error("Redis connection failed", "error", err)
		os.Exit(1)
	}
	checkClient.Close()

	asynqConfig := asynq.Config{
		Concurrency:     c.Worker.Concurrency,
		ShutdownTimeout: c.Worker.ShutdownTimeout * time.Minute,
		ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, err error) {
			var p queue.VideoTranscodingPayload
			if err := json.Unmarshal(task.Payload(), &p); err != nil {
				return
			}
			streamSvc.UpdateStreamProcessing(ctx, &pb.UpdateStreamProcessingRequest{
				StreamUuid: p.StreamUUID.String(),
				Progress:   0,
				Steps:      []string{"Processing"},
				Error:      fmt.Sprintf("Worker died or resourse limit exceeded: %v", err),
			})
		}),
	}
	return asynq.NewServer(redisClientOpt, asynqConfig)
}
