package config_test

import (
	"os"
	"testing"

	"flowforge/internal/platform/config"

	"github.com/stretchr/testify/assert"
)

func TestLoad_Defaults(t *testing.T) {
	os.Unsetenv("ENV")
	os.Unsetenv("PORT")
	os.Unsetenv("DATABASE_URL")
	os.Unsetenv("REDIS_URL")
	os.Unsetenv("LOG_LEVEL")

	cfg := config.Load()

	assert.Equal(t, "development", cfg.Environment)
	assert.Equal(t, 8080, cfg.Port)
	assert.Contains(t, cfg.DatabaseURL, "postgres://")
	assert.Contains(t, cfg.RedisURL, "redis://")
	assert.Equal(t, "info", cfg.LogLevel)
}

func TestLoad_CustomEnv(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("PORT", "9090")
	t.Setenv("LOG_LEVEL", "debug")

	cfg := config.Load()

	assert.Equal(t, "production", cfg.Environment)
	assert.Equal(t, 9090, cfg.Port)
	assert.Equal(t, "debug", cfg.LogLevel)
}
