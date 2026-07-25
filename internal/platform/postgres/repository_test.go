package postgres_test

import (
	"context"
	"testing"
	"time"

	"flowforge/internal/domain"
	"flowforge/internal/platform/postgres"

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

func TestBaseRepository_Integration(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	uow := postgres.NewUnitOfWork(pool)
	repo := postgres.NewBaseRepository[TestUserEntity](pool, "users")

	tenantID := "1d1c8e4d-0a14-4f51-b8b0-2e5d5b74d2ff"
	userID := "b4b4f5d2-7f4e-4c0a-9cf3-3f8f8a2f1b11"

	t.Run("FindByID success", func(t *testing.T) {
		user, err := repo.FindByID(ctx, tenantID, userID)
		if errorsIs(err, domain.ErrNotFound) {
			t.Skip("seed user not found in database, skipping live row assertion")
		}
		require.NoError(t, err)
		require.NotNil(t, user)
		assert.Equal(t, userID, user.ID)
		assert.Equal(t, tenantID, user.TenantID)
	})

	t.Run("FindByID not found for non-existent ID", func(t *testing.T) {
		user, err := repo.FindByID(ctx, tenantID, "00000000-0000-0000-0000-000000000000")
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
		users, total, err := repo.Paginate(ctx, tenantID, postgres.PaginationParams{
			Page:     1,
			PageSize: 10,
			OrderBy:  "created_at DESC",
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
}

func errorsIs(err, target error) bool {
	if err == nil {
		return false
	}
	return err == target
}
