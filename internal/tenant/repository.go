package tenant

import (
	"context"
	"errors"
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"flowforge/internal/domain"
	"flowforge/internal/platform/postgres"
)

var ErrTenantNotFound = errors.New("tenant not found")

// TenantRepository defines persistence operations for tenants.
type TenantRepository interface {
	FindBySlug(ctx context.Context, slug string) (*domain.Tenant, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error)
}

type postgresTenantRepository struct {
	pool *pgxpool.Pool
}

// NewTenantRepository creates a Postgres-backed TenantRepository.
func NewTenantRepository(pool *pgxpool.Pool) TenantRepository {
	return &postgresTenantRepository{
		pool: pool,
	}
}

// FindBySlug retrieves a tenant by its unique slug.
func (r *postgresTenantRepository) FindBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	dbtx := postgres.GetDBTX(ctx, r.pool)

	query, args, err := postgres.StatementBuilder.
		Select("id", "slug", "name", "created_at", "updated_at").
		From("tenants").
		Where(sq.Eq{"slug": slug}).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("build select tenant by slug query: %w", err)
	}

	rows, err := dbtx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("execute select tenant by slug query: %w", err)
	}

	tenant, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[domain.Tenant])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTenantNotFound
		}
		return nil, fmt.Errorf("scan tenant row: %w", err)
	}

	return tenant, nil
}

// FindByID retrieves a tenant by its primary key ID.
func (r *postgresTenantRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	dbtx := postgres.GetDBTX(ctx, r.pool)

	query, args, err := postgres.StatementBuilder.
		Select("id", "slug", "name", "created_at", "updated_at").
		From("tenants").
		Where(sq.Eq{"id": id.String()}).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("build select tenant by id query: %w", err)
	}

	rows, err := dbtx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("execute select tenant by id query: %w", err)
	}

	tenant, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[domain.Tenant])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTenantNotFound
		}
		return nil, fmt.Errorf("scan tenant row: %w", err)
	}

	return tenant, nil
}
