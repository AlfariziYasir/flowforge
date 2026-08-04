package logger_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"flowforge/internal/platform/logger"
)

func TestRedactURL(t *testing.T) {
	t.Run("redacts password from Redis URL with password", func(t *testing.T) {
		is := assert.New(t)
		raw := "redis://:supersecretpass@localhost:6379/0"
		redacted := logger.RedactURL(raw)
		is.Equal("redis://:*****@localhost:6379/0", redacted)
	})

	t.Run("redacts username and password from Postgres DSN URL", func(t *testing.T) {
		is := assert.New(t)
		raw := "postgres://user:dbpassword123@localhost:5432/flowforge?sslmode=disable"
		redacted := logger.RedactURL(raw)
		is.Equal("postgres://user:*****@localhost:5432/flowforge?sslmode=disable", redacted)
	})

	t.Run("leaves passwordless Redis URL intact", func(t *testing.T) {
		is := assert.New(t)
		raw := "redis://localhost:6379/0"
		redacted := logger.RedactURL(raw)
		is.Equal("redis://localhost:6379/0", redacted)
	})

	t.Run("redacts URL-encoded password from Postgres DSN URL", func(t *testing.T) {
		is := assert.New(t)
		raw := "postgres://user:p%40ssword%21@localhost:5432/flowforge?sslmode=disable"
		redacted := logger.RedactURL(raw)
		is.Equal("postgres://user:*****@localhost:5432/flowforge?sslmode=disable", redacted)
	})

	t.Run("returns original string on invalid URL", func(t *testing.T) {
		is := assert.New(t)
		raw := "invalid-url-string"
		redacted := logger.RedactURL(raw)
		is.Equal("invalid-url-string", redacted)
	})
}
