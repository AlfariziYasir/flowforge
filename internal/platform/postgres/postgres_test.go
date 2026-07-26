package postgres_test

import (
	"context"
	"testing"
	"time"

	"flowforge/internal/platform/postgres"

	"github.com/stretchr/testify/assert"
)

func TestNewPool(t *testing.T) {
	tests := []struct {
		name        string
		databaseURL string
		wantErr     bool
	}{
		{
			name:        "invalid database url format",
			databaseURL: "invalid://url::bad",
			wantErr:     true,
		},
		{
			name:        "unreachable database host",
			databaseURL: "postgres://postgres:postgres@127.0.0.1:54399/nonexistent?sslmode=disable&connect_timeout=1",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			pool, err := postgres.NewPool(ctx, tt.databaseURL)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, pool)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, pool)
				pool.Close()
			}
		})
	}
}
