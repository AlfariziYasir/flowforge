package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"flowforge/internal/auth"
	authmocks "flowforge/internal/auth/mocks"
)

func TestTokenBlacklist(t *testing.T) {
	ctx := context.Background()

	t.Run("returns false for non-revoked JTI", func(t *testing.T) {
		is := assert.New(t)
		store := authmocks.NewMockTokenBlacklist(t)
		store.EXPECT().IsRevoked(mock.Anything, "fresh-jti-123").Return(false, nil)

		revoked, err := store.IsRevoked(ctx, "fresh-jti-123")
		is.NoError(err)
		is.False(revoked)
	})

	t.Run("returns true after JTI is revoked", func(t *testing.T) {
		is := assert.New(t)
		store := authmocks.NewMockTokenBlacklist(t)
		jti := "logged-out-jti-456"

		store.EXPECT().Revoke(mock.Anything, jti, 15*time.Minute).Return(nil)
		store.EXPECT().IsRevoked(mock.Anything, jti).Return(true, nil)

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
