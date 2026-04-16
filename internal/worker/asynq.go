package worker

import (
	"time"

	"github.com/hibiken/asynq"
	"github.com/mrhumster/transcoder-service/config"
)

func NewAsynqWorker(c *config.Config) *asynq.Server {
	redisClientOpt := asynq.RedisClientOpt{
		Addr:     c.Redis.Addr,
		Password: c.Redis.Passwrod,
		DB:       c.Redis.DB,
	}
	asynqConfig := asynq.Config{
		Concurrency:     c.Worker.Concurrency,
		ShutdownTimeout: c.Worker.ShutdownTimeout * time.Minute,
	}
	return asynq.NewServer(redisClientOpt, asynqConfig)
}
