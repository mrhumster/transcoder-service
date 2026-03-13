package config

import (
	"os"
	"strconv"
)

type Config struct {
	Redis Redis
	MinIO MinIO
}

type Redis struct {
	Addr     string
	Passwrod string
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
	useSSL, _ := strconv.ParseBool(getEnv("MINIO_USE_SSL", "false"))
	return &Config{
		Redis: Redis{
			Addr:     getEnv("REDIS_ADDR", "localhost"),
			Passwrod: getEnv("REDIS_PASS", ""),
		},
		MinIO: MinIO{
			Endpoint:        getEnv("MINIO_ENDPOINT", "localhost:9000"),
			AccessKeyID:     getEnv("MINIO_ACCESS_KEY", "admin"),
			SecretAccessKey: getEnv("MINIO_SECRET_KEY", "minio123"),
			BucketName:      getEnv("MINIO_BUCKET_NAME", "stream-service-test"),
			UseSSL:          useSSL,
			Region:          getEnv("MINIO_REGION", "ru-east-1"),
		},
	}, nil
}

func getEnv(key, defaultValue string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultValue
}
