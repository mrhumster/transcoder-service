package config

import (
	"log/slog"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Redis  Redis
	MinIO  MinIO
	Server Server
	Worker Worker
}

type Worker struct {
	Concurrency     int
	ShutdownTimeout time.Duration
}

type Server struct {
	StreamSeviceAddr string
}

type Redis struct {
	Addr     string
	Passwrod string
	DB       int
}

type MinIO struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	BucketName      string
	UseSSL          bool
	Region          string
}

func LoadConfig() (*Config, error) {
	useSSL, err := strconv.ParseBool(getEnv("MINIO_USE_SSL", "false"))
	if err != nil {
		slog.Error("Minio use SSL from config parse failed. The defaul value is `False`", "error", err)
		useSSL = false
	}
	redisDB, err := strconv.ParseUint(getEnv("REDIS_DB", "2"), 10, 32)
	if err != nil {
		slog.Error("Redis DB from config parse failed. The dafault value is `2`", "error", err)
		redisDB = 2
	}
	concurrency, err := strconv.ParseUint(getEnv("WORKER_CONCURRENCY", "1"), 10, 32)
	if err != nil {
		slog.Error("Worker concurrency from config parse failed. The defaul value is `1`", "error", err)
		concurrency = 1
	}

	shutdownTimeout, err := time.ParseDuration(getEnv("WORKER_SHUTDOWN_TIMEOUT", "50m"))
	if err != nil {
		slog.Error("Worker shutdown timeout from config parse failed. The defaul value is `50m`")
		shutdownTimeout = 50 * time.Minute
	}

	return &Config{
		Redis: Redis{
			Addr:     getEnv("REDIS_ADDR", "localhost"),
			Passwrod: getEnv("redis-password", ""),
			DB:       int(redisDB),
		},
		MinIO: MinIO{
			Endpoint:        getEnv("MINIO_ENDPOINT", "localhost:9000"),
			AccessKeyID:     getEnv("MINIO_ACCESS_KEY", "admin"),
			SecretAccessKey: getEnv("MINIO_SECRET_KEY", "minio123"),
			BucketName:      getEnv("MINIO_BUCKET_NAME", "stream-service-test"),
			UseSSL:          useSSL,
			Region:          getEnv("MINIO_REGION", "ru-east-1"),
		},
		Server: Server{
			StreamSeviceAddr: getEnv("STREAM_SERVICE_ADDR", "localhost:50051"),
		},
		Worker: Worker{
			Concurrency:     int(concurrency),
			ShutdownTimeout: shutdownTimeout,
		},
	}, nil
}

func getEnv(key, defaultValue string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultValue
}
