package config_test

import (
	"os"
	"testing"
	"time"

	"flowforge/internal/platform/config"

	"github.com/stretchr/testify/assert"
)

func TestLoad_Defaults(t *testing.T) {
	os.Unsetenv("ENV")
	os.Unsetenv("PORT")
	os.Unsetenv("DATABASE_URL")
	os.Unsetenv("REDIS_URL")
	os.Unsetenv("LOG_LEVEL")
	os.Unsetenv("JWT_ACCESS_EXPIRY")
	os.Unsetenv("JWT_REFRESH_EXPIRY")

	cfg := config.Load()

	assert.Equal(t, "development", cfg.Environment)
	assert.Equal(t, 8080, cfg.Port)
	assert.Contains(t, cfg.DatabaseURL, "postgres://")
	assert.Contains(t, cfg.RedisURL, "redis://")
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, 15*time.Minute, cfg.JWTAccessExpiry)
	assert.Equal(t, 7*24*time.Hour, cfg.JWTRefreshExpiry)
	assert.Equal(t, "", cfg.JWTSecret)
}

func TestLoad_CustomEnv(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("PORT", "9090")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("JWT_ACCESS_EXPIRY", "30m")
	t.Setenv("JWT_REFRESH_EXPIRY", "72h")

	cfg := config.Load()

	assert.Equal(t, "production", cfg.Environment)
	assert.Equal(t, 9090, cfg.Port)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, 30*time.Minute, cfg.JWTAccessExpiry)
	assert.Equal(t, 72*time.Hour, cfg.JWTRefreshExpiry)
}
