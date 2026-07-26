package auth_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"flowforge/internal/auth"
)

func TestAuthUserContext(t *testing.T) {
	t.Run("injects and retrieves AuthUser from context", func(t *testing.T) {
		is := assert.New(t)

		user := auth.AuthUser{
			ID:       uuid.New(),
			TenantID: uuid.New(),
			Email:    "admin@flowforge.local",
			Role:     "admin",
		}

		ctx := auth.ContextWithAuthUser(context.Background(), user)

		gotUser, ok := auth.AuthUserFromContext(ctx)
		is.True(ok)
		is.Equal(user, gotUser)
	})

	t.Run("returns false when AuthUser is not in context", func(t *testing.T) {
		is := assert.New(t)

		_, ok := auth.AuthUserFromContext(context.Background())
		is.False(ok)
	})

	t.Run("extracts TenantID from context when present", func(t *testing.T) {
		is := assert.New(t)

		tenantID := uuid.New()
		user := auth.AuthUser{
			ID:       uuid.New(),
			TenantID: tenantID,
			Email:    "editor@flowforge.local",
			Role:     "editor",
		}

		ctx := auth.ContextWithAuthUser(context.Background(), user)

		gotTenantID := auth.TenantIDFromContext(ctx)
		is.Equal(tenantID, gotTenantID)
	})

	t.Run("returns uuid.Nil when TenantID is missing from context", func(t *testing.T) {
		is := assert.New(t)

		gotTenantID := auth.TenantIDFromContext(context.Background())
		is.Equal(uuid.Nil, gotTenantID)
	})
}
