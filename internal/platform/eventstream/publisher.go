package eventstream

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"flowforge/internal/domain"
)

// Publisher publishes real-time platform events to an event stream.
type Publisher interface {
	Publish(ctx context.Context, event domain.Event) error
}

type redisPublisher struct {
	rdb *redis.Client
}

// NewRedisPublisher creates a Publisher backed by Redis Pub/Sub.
func NewRedisPublisher(rdb *redis.Client) Publisher {
	if rdb == nil {
		return NewNoopPublisher()
	}
	return &redisPublisher{rdb: rdb}
}

func (p *redisPublisher) Publish(ctx context.Context, event domain.Event) error {
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	channel := ChannelForTenant(event.TenantID)
	if err := p.rdb.Publish(ctx, channel, data).Err(); err != nil {
		return fmt.Errorf("redis publish to %s: %w", channel, err)
	}

	return nil
}

type noopPublisher struct{}

// NewNoopPublisher returns a Publisher that silently discards events.
func NewNoopPublisher() Publisher {
	return &noopPublisher{}
}

func (noopPublisher) Publish(ctx context.Context, event domain.Event) error {
	return nil
}

// ChannelForTenant formats the Redis Pub/Sub channel name for a tenant.
func ChannelForTenant(tenantID uuid.UUID) string {
	return fmt.Sprintf("events:tenant:%s", tenantID)
}
