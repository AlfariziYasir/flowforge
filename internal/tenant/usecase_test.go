package tenant_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"flowforge/internal/domain"
	"flowforge/internal/tenant"
	tenantmocks "flowforge/internal/tenant/mocks"
)

func TestTenantUseCase_GetBySlug(t *testing.T) {
	ctx := context.Background()

	tnt := &domain.Tenant{
		ID:        uuid.New(),
		Slug:      "default-tenant",
		Name:      "Default Tenant",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	t.Run("returns tenant when slug exists and normalizes input", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		repo := tenantmocks.NewMockTenantRepository(t)
		repo.EXPECT().FindBySlug(mock.Anything, "default-tenant").Return(tnt, nil)

		uc := tenant.NewTenantUseCase(repo)

		got, err := uc.GetBySlug(ctx, " DEFAULT-TENANT ")
		req.NoError(err)
		is.Equal(tnt.ID, got.ID)
		is.Equal("default-tenant", got.Slug)
	})

	t.Run("returns ErrTenantNotFound when slug does not exist", func(t *testing.T) {
		is := assert.New(t)

		repo := tenantmocks.NewMockTenantRepository(t)
		repo.EXPECT().FindBySlug(mock.Anything, "nonexistent").Return(nil, tenant.ErrTenantNotFound)

		uc := tenant.NewTenantUseCase(repo)

		_, err := uc.GetBySlug(ctx, "nonexistent")
		is.ErrorIs(err, tenant.ErrTenantNotFound)
	})
}

func TestTenantUseCase_GetByID(t *testing.T) {
	ctx := context.Background()

	tnt := &domain.Tenant{
		ID:   uuid.New(),
		Slug: "acme-corp",
		Name: "Acme Corporation",
	}

	t.Run("returns tenant by ID", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		repo := tenantmocks.NewMockTenantRepository(t)
		repo.EXPECT().FindByID(mock.Anything, tnt.ID).Return(tnt, nil)

		uc := tenant.NewTenantUseCase(repo)

		got, err := uc.GetByID(ctx, tnt.ID)
		req.NoError(err)
		is.Equal("acme-corp", got.Slug)
	})

	t.Run("returns ErrTenantNotFound for unknown ID", func(t *testing.T) {
		is := assert.New(t)

		unknownID := uuid.New()
		repo := tenantmocks.NewMockTenantRepository(t)
		repo.EXPECT().FindByID(mock.Anything, unknownID).Return(nil, tenant.ErrTenantNotFound)

		uc := tenant.NewTenantUseCase(repo)

		_, err := uc.GetByID(ctx, unknownID)
		is.ErrorIs(err, tenant.ErrTenantNotFound)
	})
}
