package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"flowforge/internal/auth"
)

type mockBlacklistStore struct {
	revoked map[string]time.Time
}

func newMockBlacklistStore() *mockBlacklistStore {
	return &mockBlacklistStore{
		revoked: make(map[string]time.Time),
	}
}

func (m *mockBlacklistStore) Revoke(ctx context.Context, jti string, ttl time.Duration) error {
	m.revoked[jti] = time.Now().Add(ttl)
	return nil
}

func (m *mockBlacklistStore) IsRevoked(ctx context.Context, jti string) (bool, error) {
	exp, found := m.revoked[jti]
	if !found {
		return false, nil
	}
	if time.Now().After(exp) {
		delete(m.revoked, jti)
		return false, nil
	}
	return true, nil
}

func TestTokenBlacklist(t *testing.T) {
	ctx := context.Background()
	store := newMockBlacklistStore()

	t.Run("returns false for non-revoked JTI", func(t *testing.T) {
		is := assert.New(t)

		revoked, err := store.IsRevoked(ctx, "fresh-jti-123")
		is.NoError(err)
		is.False(revoked)
	})

	t.Run("returns true after JTI is revoked", func(t *testing.T) {
		is := assert.New(t)

		jti := "logged-out-jti-456"
		err := store.Revoke(ctx, jti, 15*time.Minute)
		is.NoError(err)

		revoked, err := store.IsRevoked(ctx, jti)
		is.NoError(err)
		is.True(revoked)
	})
}

func TestNoopTokenBlacklist(t *testing.T) {
	ctx := context.Background()
	bl := auth.NewNoopTokenBlacklist()

	t.Run("noop blacklist never blocks or errors", func(t *testing.T) {
		is := assert.New(t)

		err := bl.Revoke(ctx, "some-jti", 15*time.Minute)
		is.NoError(err)

		revoked, err := bl.IsRevoked(ctx, "some-jti")
		is.NoError(err)
		is.False(revoked)
	})
}
