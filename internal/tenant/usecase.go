package tenant

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"flowforge/internal/domain"
)

// TenantUseCase defines pure business operations for tenants.
type TenantUseCase interface {
	GetBySlug(ctx context.Context, slug string) (*domain.Tenant, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error)
}

type tenantUseCase struct {
	repo TenantRepository
}

// NewTenantUseCase creates a new TenantUseCase instance.
func NewTenantUseCase(repo TenantRepository) TenantUseCase {
	return &tenantUseCase{
		repo: repo,
	}
}

// GetBySlug retrieves a tenant by normalized slug.
func (u *tenantUseCase) GetBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	sanitizedSlug := strings.ToLower(strings.TrimSpace(slug))
	if sanitizedSlug == "" {
		return nil, ErrTenantNotFound
	}
	return u.repo.FindBySlug(ctx, sanitizedSlug)
}

// GetByID retrieves a tenant by ID.
func (u *tenantUseCase) GetByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	if id == uuid.Nil {
		return nil, ErrTenantNotFound
	}
	return u.repo.FindByID(ctx, id)
}
