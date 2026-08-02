package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"flowforge/internal/auth"
	authmocks "flowforge/internal/auth/mocks"
	"flowforge/internal/domain"
)

func TestUserRepository_FindByEmail(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	user := &domain.User{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Email:        "admin@flowforge.local",
		PasswordHash: "$2a$12$hash",
		Role:         "admin",
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	t.Run("returns user when email and tenant ID match regardless of casing", func(t *testing.T) {
		is := assert.New(t)
		repo := authmocks.NewMockUserRepository(t)
		repo.EXPECT().FindByEmail(mock.Anything, tenantID, "admin@flowforge.local").Return(user, nil)

		got, err := repo.FindByEmail(ctx, tenantID, "admin@flowforge.local")
		is.NoError(err)
		is.Equal(user.ID, got.ID)
		is.Equal("admin@flowforge.local", got.Email)
	})

	t.Run("returns ErrUserNotFound when email does not exist", func(t *testing.T) {
		is := assert.New(t)
		repo := authmocks.NewMockUserRepository(t)
		repo.EXPECT().FindByEmail(mock.Anything, tenantID, "nonexistent@flowforge.local").Return(nil, auth.ErrUserNotFound)

		_, err := repo.FindByEmail(ctx, tenantID, "nonexistent@flowforge.local")
		is.ErrorIs(err, auth.ErrUserNotFound)
	})

	t.Run("enforces tenant isolation — wrong tenant ID returns ErrUserNotFound", func(t *testing.T) {
		is := assert.New(t)
		repo := authmocks.NewMockUserRepository(t)
		wrongTenantID := uuid.New()
		repo.EXPECT().FindByEmail(mock.Anything, wrongTenantID, "admin@flowforge.local").Return(nil, auth.ErrUserNotFound)

		_, err := repo.FindByEmail(ctx, wrongTenantID, "admin@flowforge.local")
		is.ErrorIs(err, auth.ErrUserNotFound)
	})
}
