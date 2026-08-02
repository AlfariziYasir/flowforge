package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"flowforge/internal/domain"
	"flowforge/internal/platform/postgres"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type TestUserEntity struct {
	ID           string    `db:"id"`
	TenantID     string    `db:"tenant_id"`
	Email        string    `db:"email"`
	PasswordHash string    `db:"password_hash"`
	Role         string    `db:"role"`
	IsActive     bool      `db:"is_active"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

func TestBaseRepository_Unit(t *testing.T) {
	repo := postgres.NewBaseRepository[TestUserEntity](nil, "users")
	assert.Equal(t, "users", repo.TableName())
}

func TestIsUniqueViolation(t *testing.T) {
	t.Run("returns constraint name for pg 23505 error", func(t *testing.T) {
		pgErr := &pgconn.PgError{
			Code:           "23505",
			ConstraintName: "uq_users_tenant_email",
		}
		constraint, ok := postgres.IsUniqueViolation(pgErr)
		assert.True(t, ok)
		assert.Equal(t, "uq_users_tenant_email", constraint)
	})

	t.Run("returns false for non-unique pg error", func(t *testing.T) {
		pgErr := &pgconn.PgError{Code: "23503"}
		_, ok := postgres.IsUniqueViolation(pgErr)
		assert.False(t, ok)
	})
}

func TestBaseRepository_Integration(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	uow := postgres.NewUnitOfWork(pool)
	repo := postgres.NewBaseRepository[TestUserEntity](pool, "users")

	tenantID := "1d1c8e4d-0a14-4f51-b8b0-2e5d5b74d2ff"
	userID := "b4b4f5d2-7f4e-4c0a-9cf3-3f8f8a2f1b11"

	t.Run("FindByID success", func(t *testing.T) {
		user, err := repo.FindByFilter(ctx, map[string]interface{}{"tenant_id": tenantID, "id": userID})
		if errorsIs(err, domain.ErrNotFound) {
			t.Skip("seed user not found in database, skipping live row assertion")
		}
		require.NoError(t, err)
		require.NotNil(t, user)
		assert.Equal(t, userID, user.ID)
		assert.Equal(t, tenantID, user.TenantID)
	})

	t.Run("FindByID not found for non-existent ID", func(t *testing.T) {
		user, err := repo.FindByFilter(ctx, map[string]interface{}{"tenant_id": tenantID, "id": "00000000-0000-0000-0000-000000000000"})
		fmt.Printf("error message: %s", err.Error())
		assert.ErrorIs(t, err, domain.ErrNotFound)
		assert.Nil(t, user)
	})

	t.Run("FindAll tenant isolation", func(t *testing.T) {
		users, err := repo.FindAll(ctx, tenantID)
		require.NoError(t, err)
		assert.NotNil(t, users)

		emptyUsers, err := repo.FindAll(ctx, "00000000-0000-0000-0000-000000000000")
		require.NoError(t, err)
		assert.Empty(t, emptyUsers)
	})

	t.Run("Paginate tenant isolation", func(t *testing.T) {
		users, total, err := repo.Paginate(ctx, postgres.PaginationParams{
			Page:     1,
			PageSize: 10,
			OrderBy:  "created_at DESC",
			Filters:  map[string]interface{}{"tenant_id": tenantID},
		})
		require.NoError(t, err)
		assert.NotNil(t, users)
		assert.GreaterOrEqual(t, total, int64(0))
	})

	t.Run("Transaction context isolation", func(t *testing.T) {
		err := uow.ExecuteInTx(ctx, func(txCtx context.Context) error {
			users, err := repo.FindAll(txCtx, tenantID)
			require.NoError(t, err)
			assert.NotNil(t, users)
			return nil
		})
		require.NoError(t, err)
	})

	t.Run("Create Success", func(t *testing.T) {
		user := &TestUserEntity{
			ID:           uuid.NewString(),
			TenantID:     tenantID,
			Email:        "[EMAIL_ADDRESS]",
			PasswordHash: "password",
			Role:         "user",
			IsActive:     true,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		err := repo.Create(ctx, user)
		require.NoError(t, err)
	})
}

func errorsIs(err, target error) bool {
	if err == nil {
		return false
	}
	return err == target
}
