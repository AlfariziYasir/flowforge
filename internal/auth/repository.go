package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"flowforge/internal/domain"
	"flowforge/internal/platform/postgres"
)

var (
	ErrUserNotFound      = errors.New("user not found")
	ErrUserAlreadyExists = errors.New("user already exists")
	ErrUserInactive      = errors.New("user account is inactive")
	ErrInvalidRole       = errors.New("invalid user role")
)

type UserRepository interface {
	FindByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*domain.User, error)
	FindByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.User, error)
	CreateUser(ctx context.Context, user *domain.User) error
	UpdateUser(ctx context.Context, user *domain.User) error
	ListUsers(ctx context.Context, tenantID uuid.UUID, page, pageSize int, orderby, role string, isAsc, activeOnly *bool) ([]*domain.User, int64, error)
}

type postgresUserRepository struct {
	base *postgres.BaseRepository[domain.User]
}

func NewUserRepository(pool *pgxpool.Pool) UserRepository {
	return &postgresUserRepository{
		base: postgres.NewBaseRepository[domain.User](pool, "users"),
	}
}

func (r *postgresUserRepository) FindByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*domain.User, error) {
	sanitizedEmail := strings.ToLower(strings.TrimSpace(email))
	filters := map[string]interface{}{
		"tenant_id": tenantID.String(),
		"email":     sanitizedEmail,
	}
	user, err := r.base.FindByFilter(ctx, filters)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	return user, nil
}

func (r *postgresUserRepository) FindByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.User, error) {
	filters := map[string]interface{}{
		"tenant_id": tenantID.String(),
		"id":        id.String(),
	}
	user, err := r.base.FindByFilter(ctx, filters)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	return user, nil
}

func (r *postgresUserRepository) ListUsers(ctx context.Context, tenantID uuid.UUID, page, pageSize int, orderby, role string, isAsc, activeOnly *bool) ([]*domain.User, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	params := postgres.PaginationParams{
		Page:     page,
		PageSize: pageSize,
	}

	if orderby != "" {
		direction := "desc"
		if isAsc != nil && *isAsc {
			direction = "asc"
		}
		params.OrderBy = fmt.Sprintf("%s %s", orderby, direction)
	}

	params.Filters = map[string]interface{}{
		"tenant_id": tenantID.String(),
	}
	if role != "" {
		params.Filters["role"] = role
	}
	if activeOnly != nil {
		params.Filters["is_active"] = *activeOnly
	}

	users, total, err := r.base.Paginate(ctx, params)
	if err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

func (r *postgresUserRepository) CreateUser(ctx context.Context, user *domain.User) error {
	if user.ID == uuid.Nil {
		user.ID = uuid.New()
	}
	user.Email = strings.ToLower(strings.TrimSpace(user.Email))
	now := time.Now()
	if user.CreatedAt.IsZero() {
		user.CreatedAt = now
	}
	user.UpdatedAt = now

	err := r.base.Create(ctx, user)
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return ErrUserAlreadyExists
		}
		return err
	}

	return nil
}

func (r *postgresUserRepository) UpdateUser(ctx context.Context, user *domain.User) error {
	user.Email = strings.ToLower(strings.TrimSpace(user.Email))
	user.UpdatedAt = time.Now()
	err := r.base.Update(ctx, user.TenantID.String(), user.ID.String(), user)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ErrUserNotFound
		}
		if errors.Is(err, domain.ErrConflict) {
			return ErrUserAlreadyExists
		}
		return err
	}

	return nil
}
