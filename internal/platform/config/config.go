package config

import (
	"os"
	"strconv"
	"strings"
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

	WorkerConcurrency int
	RunLeaseDuration  time.Duration
	StepMaxBodyBytes  int64

	AIProviderAPIKey string
	AIModel          string
	AIBaseURL        string
	AIRequestTimeout time.Duration
	AIMaxRetries     int

	GRPCPort   string
	NATSURL    string
	NATSStream string

	CORSAllowedOrigins   []string
	CORSAllowCredentials bool
	MetricsPort          int
}

// Load populates configuration from environment variables with fallback defaults.
func Load() *Config {
	return &Config{
		Environment:       getEnv("ENV", "development"),
		Port:              getEnvInt("PORT", 8080),
		DatabaseURL:       getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/flowforge?sslmode=disable"),
		RedisURL:          getEnv("REDIS_URL", "redis://localhost:6379"),
		LogLevel:          getEnv("LOG_LEVEL", "info"),
		AllowedHTTP:       getEnv("ALLOWED_HTTP_HOSTS", ""),
		JWTSecret:         getEnv("JWT_SECRET", ""),
		JWTAccessExpiry:   getEnvDuration("JWT_ACCESS_EXPIRY", 15*time.Minute),
		JWTRefreshExpiry:  getEnvDuration("JWT_REFRESH_EXPIRY", 7*24*time.Hour),
		TrustProxyHeaders: getEnvBool("TRUST_PROXY_HEADERS", false),

		WorkerConcurrency: getEnvInt("WORKER_CONCURRENCY", 10),
		RunLeaseDuration:  getEnvDuration("RUN_LEASE_DURATION", 60*time.Second),
		StepMaxBodyBytes:  getEnvInt64("STEP_MAX_BODY_BYTES", 1<<20),

		AIProviderAPIKey: getEnv("AI_PROVIDER_API_KEY", ""),
		AIModel:          getEnv("AI_MODEL", "deepseek-chat"),
		AIBaseURL:        getEnv("AI_BASE_URL", "https://api.deepseek.com"),
		AIRequestTimeout: getEnvDuration("AI_REQUEST_TIMEOUT", 30*time.Second),
		AIMaxRetries:     getEnvInt("AI_MAX_RETRIES", 2),

		GRPCPort:   getEnv("GRPC_PORT", "9090"),
		NATSURL:    getEnv("NATS_URL", "nats://localhost:4222"),
		NATSStream: getEnv("NATS_STREAM", "FLOWFORGE_EVENTS"),

		CORSAllowedOrigins:   getEnvSlice("CORS_ALLOWED_ORIGINS", nil),
		CORSAllowCredentials: getEnvBool("CORS_ALLOW_CREDENTIALS", false),
		MetricsPort:          getEnvInt("METRICS_PORT", 9091),
	}
}

func getEnvSlice(key string, fallback []string) []string {
	if val := os.Getenv(key); val != "" {
		parts := strings.Split(val, ",")
		var res []string
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				res = append(res, s)
			}
		}
		return res
	}
	return fallback
}

func getEnvInt64(key string, fallback int64) int64 {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.ParseInt(val, 10, 64); err == nil {
			return i
		}
	}
	return fallback
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
