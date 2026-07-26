package tenant_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"flowforge/internal/domain"
	"flowforge/internal/tenant"
)

type MockTenantRepository struct {
	tenants map[string]*domain.Tenant
}

func NewMockTenantRepository() *MockTenantRepository {
	return &MockTenantRepository{
		tenants: make(map[string]*domain.Tenant),
	}
}

func (m *MockTenantRepository) FindBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	t, exists := m.tenants[slug]
	if !exists {
		return nil, tenant.ErrTenantNotFound
	}
	return t, nil
}

func (m *MockTenantRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	for _, t := range m.tenants {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, tenant.ErrTenantNotFound
}

func (m *MockTenantRepository) Save(ctx context.Context, t *domain.Tenant) error {
	m.tenants[t.Slug] = t
	return nil
}

func TestTenantRepository_FindBySlug(t *testing.T) {
	repo := NewMockTenantRepository()
	ctx := context.Background()

	tnt := &domain.Tenant{
		ID:        uuid.New(),
		Slug:      "default-tenant",
		Name:      "Default Workspace",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	err := repo.Save(ctx, tnt)
	assert.NoError(t, err)

	t.Run("returns tenant when slug matches", func(t *testing.T) {
		is := assert.New(t)

		got, err := repo.FindBySlug(ctx, "default-tenant")
		is.NoError(err)
		is.Equal(tnt.ID, got.ID)
		is.Equal("Default Workspace", got.Name)
	})

	t.Run("returns ErrTenantNotFound when slug does not exist", func(t *testing.T) {
		is := assert.New(t)

		_, err := repo.FindBySlug(ctx, "unknown-tenant")
		is.Equal(tenant.ErrTenantNotFound, err)
	})
}
