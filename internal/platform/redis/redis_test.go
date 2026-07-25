package redis_test

import (
	"context"
	"testing"
	"time"

	"flowforge/internal/platform/redis"

	"github.com/stretchr/testify/assert"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name     string
		redisURL string
		wantErr  bool
	}{
		{
			name:     "invalid redis url format",
			redisURL: "invalid://url::bad",
			wantErr:  true,
		},
		{
			name:     "unreachable redis server",
			redisURL: "redis://127.0.0.1:54399",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()

			client, err := redis.NewClient(ctx, tt.redisURL)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, client)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, client)
				_ = client.Close()
			}
		})
	}
}
