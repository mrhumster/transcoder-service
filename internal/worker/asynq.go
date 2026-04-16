package worker

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mrhumster/transcoder-service/config"
	"github.com/redis/go-redis/v9"
)

func NewAsynqWorker(c *config.Config) *asynq.Server {
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
	}
	return asynq.NewServer(redisClientOpt, asynqConfig)
}
