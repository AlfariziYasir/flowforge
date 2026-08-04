package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds runtime configuration settings for FlowForge services.
type Config struct {
	Environment       string
	Port              int
	DatabaseURL       string
	RedisURL          string
	LogLevel          string
	AllowedHTTP       string
	JWTSecret         string
	JWTAccessExpiry   time.Duration
	JWTRefreshExpiry  time.Duration
	TrustProxyHeaders bool
}

// Load populates configuration from environment variables with fallback defaults.
func Load() *Config {
	return &Config{
		Environment:       getEnv("ENV", "development"),
		Port:              getEnvInt("PORT", 8080),
		DatabaseURL:       getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/flowforge?sslmode=disable"),
		RedisURL:          getEnv("REDIS_URL", "redis://localhost:6379"),
		LogLevel:          getEnv("LOG_LEVEL", "info"),
		AllowedHTTP:       getEnv("ALLOWED_HTTP_HOSTS", "httpbin.org,localhost,127.0.0.1"),
		JWTSecret:         getEnv("JWT_SECRET", ""),
		JWTAccessExpiry:   getEnvDuration("JWT_ACCESS_EXPIRY", 15*time.Minute),
		JWTRefreshExpiry:  getEnvDuration("JWT_REFRESH_EXPIRY", 7*24*time.Hour),
		TrustProxyHeaders: getEnvBool("TRUST_PROXY_HEADERS", false),
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

func getEnvBool(key string, fallback bool) bool {
	if val := os.Getenv(key); val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			return b
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return fallback
}
