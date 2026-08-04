package auth

import (
	"context"
	"fmt"
	"time"

	redisclient "github.com/redis/go-redis/v9"
)

const blacklistKeyPrefix = "token:revoked:"

// TokenBlacklist handles revoking and checking revoked JWT JTIs.
type TokenBlacklist interface {
	Revoke(ctx context.Context, jti string, ttl time.Duration) error
	IsRevoked(ctx context.Context, jti string) (bool, error)
}

type redisTokenBlacklist struct {
	client *redisclient.Client
}

// NewRedisTokenBlacklist creates a Redis-backed TokenBlacklist.
func NewRedisTokenBlacklist(client *redisclient.Client) TokenBlacklist {
	if client == nil {
		return NewNoopTokenBlacklist()
	}
	return &redisTokenBlacklist{
		client: client,
	}
}

// Revoke adds a JTI to the Redis blacklist with a TTL equal to token remaining expiration.
func (b *redisTokenBlacklist) Revoke(ctx context.Context, jti string, ttl time.Duration) error {
	if jti == "" {
		return nil
	}
	if ttl <= 0 {
		return nil
	}

	key := blacklistKeyPrefix + jti
	if err := b.client.Set(ctx, key, "1", ttl).Err(); err != nil {
		return fmt.Errorf("revoke token jti in redis: %w", err)
	}

	return nil
}

// IsRevoked checks if a JTI exists in the Redis blacklist.
func (b *redisTokenBlacklist) IsRevoked(ctx context.Context, jti string) (bool, error) {
	if jti == "" {
		return false, nil
	}

	key := blacklistKeyPrefix + jti
	exists, err := b.client.Exists(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("check token jti revoked in redis: %w", err)
	}

	return exists > 0, nil
}

type noopTokenBlacklist struct{}

// NewNoopTokenBlacklist creates a no-op TokenBlacklist implementation.
func NewNoopTokenBlacklist() TokenBlacklist {
	return &noopTokenBlacklist{}
}

func (n *noopTokenBlacklist) Revoke(ctx context.Context, jti string, ttl time.Duration) error {
	return nil
}

func (n *noopTokenBlacklist) IsRevoked(ctx context.Context, jti string) (bool, error) {
	return false, nil
}
