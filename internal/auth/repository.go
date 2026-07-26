package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// UserRepository defines persistence operations for users.
type UserRepository interface {
	FindByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*domain.User, error)
	FindByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.User, error)
	CreateUser(ctx context.Context, user *domain.User) error
	UpdateUser(ctx context.Context, user *domain.User) error
}

type postgresUserRepository struct {
	base *postgres.BaseRepository[domain.User]
}

// NewUserRepository creates a Postgres-backed UserRepository.
func NewUserRepository(pool *pgxpool.Pool) UserRepository {
	return &postgresUserRepository{
		base: postgres.NewBaseRepository[domain.User](pool, "users"),
	}
}

// FindByEmail retrieves a user by tenant ID and email (case insensitive).
func (r *postgresUserRepository) FindByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*domain.User, error) {
	dbtx := r.base.GetDB(ctx)

	sanitizedEmail := strings.ToLower(strings.TrimSpace(email))

	query, args, err := postgres.StatementBuilder.
		Select("id", "tenant_id", "email", "password_hash", "role", "is_active", "created_at", "updated_at").
		From("users").
		Where(sq.Eq{"tenant_id": tenantID.String(), "email": sanitizedEmail}).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("build select user by email query: %w", err)
	}

	rows, err := dbtx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("execute select user by email query: %w", err)
	}

	user, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[domain.User])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("scan user row: %w", err)
	}

	return user, nil
}

// FindByID retrieves a user by tenant ID and user ID.
func (r *postgresUserRepository) FindByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.User, error) {
	user, err := r.base.FindByID(ctx, tenantID.String(), id.String())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, domain.ErrNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return user, nil
}

// CreateUser inserts a new tenant user with sanitized email.
func (r *postgresUserRepository) CreateUser(ctx context.Context, user *domain.User) error {
	dbtx := r.base.GetDB(ctx)

	if user.ID == uuid.Nil {
		user.ID = uuid.New()
	}
	user.Email = strings.ToLower(strings.TrimSpace(user.Email))
	now := time.Now()
	if user.CreatedAt.IsZero() {
		user.CreatedAt = now
	}
	user.UpdatedAt = now

	query, args, err := postgres.StatementBuilder.
		Insert("users").
		Columns("id", "tenant_id", "email", "password_hash", "role", "is_active", "created_at", "updated_at").
		Values(user.ID.String(), user.TenantID.String(), user.Email, user.PasswordHash, user.Role, user.IsActive, user.CreatedAt, user.UpdatedAt).
		ToSql()
	if err != nil {
		return fmt.Errorf("build insert user query: %w", err)
	}

	_, err = dbtx.Exec(ctx, query, args...)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // Unique violation
			return ErrUserAlreadyExists
		}
		return fmt.Errorf("execute insert user query: %w", err)
	}

	return nil
}

// UpdateUser updates an existing tenant user's record.
func (r *postgresUserRepository) UpdateUser(ctx context.Context, user *domain.User) error {
	dbtx := r.base.GetDB(ctx)

	user.Email = strings.ToLower(strings.TrimSpace(user.Email))
	user.UpdatedAt = time.Now()

	query, args, err := postgres.StatementBuilder.
		Update("users").
		Set("email", user.Email).
		Set("password_hash", user.PasswordHash).
		Set("role", user.Role).
		Set("is_active", user.IsActive).
		Set("updated_at", user.UpdatedAt).
		Where(sq.Eq{"tenant_id": user.TenantID.String(), "id": user.ID.String()}).
		ToSql()
	if err != nil {
		return fmt.Errorf("build update user query: %w", err)
	}

	tag, err := dbtx.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("execute update user query: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}

	return nil
}
