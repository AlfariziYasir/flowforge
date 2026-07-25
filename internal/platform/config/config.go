package config

import (
	"os"
	"strconv"
)

// Config holds runtime configuration settings for FlowForge services.
type Config struct {
	Environment string
	Port        int
	DatabaseURL string
	RedisURL    string
	LogLevel    string
	AllowedHTTP string
}

// Load populates configuration from environment variables with fallback defaults.
func Load() *Config {
	return &Config{
		Environment: getEnv("ENV", "development"),
		Port:        getEnvInt("PORT", 8080),
		DatabaseURL: getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/flowforge?sslmode=disable"),
		RedisURL:    getEnv("REDIS_URL", "redis://localhost:6379"),
		LogLevel:    getEnv("LOG_LEVEL", "info"),
		AllowedHTTP: getEnv("ALLOWED_HTTP_HOSTS", "httpbin.org,localhost,127.0.0.1"),
	}
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return fallback
}
