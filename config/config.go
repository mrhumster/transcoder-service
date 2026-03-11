package config

import "os"

type Config struct {
	Redis Redis
}

type Redis struct {
	Addr     string
	Passwrod string
}

func LoadConfig() (*Config, error) {
	return &Config{
		Redis: Redis{
			Addr:     getEnv("REDIS_ADDR", "localhost"),
			Passwrod: getEnv("REDIS_PASS", ""),
		},
	}, nil
}

func getEnv(key, defaultValue string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultValue
}
