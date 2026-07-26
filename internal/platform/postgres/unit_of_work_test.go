package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"flowforge/internal/platform/postgres"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
)

func TestContextTxHelpers(t *testing.T) {
	ctx := context.Background()

	// Verify initially no transaction in context
	tx, ok := postgres.TxFromContext(ctx)
	assert.False(t, ok)
	assert.Nil(t, tx)

	// Verify GetDBTX falls back to pool when context has no transaction
	db := postgres.GetDBTX(ctx, nil)
	assert.Nil(t, db)
}

func getTestPool(t *testing.T) *pgxpool.Pool {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/flowforge?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool, err := postgres.NewPool(ctx, dbURL)
	if err != nil {
		t.Skipf("skipping live database test: %v", err)
	}
	return pool
}

func TestUnitOfWork_ExecuteInTx(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()

	t.Run("successful commit", func(t *testing.T) {
		err := uow.ExecuteInTx(ctx, func(txCtx context.Context) error {
			tx, ok := postgres.TxFromContext(txCtx)
			assert.True(t, ok)
			assert.NotNil(t, tx)

			db := postgres.GetDBTX(txCtx, pool)
			assert.Equal(t, tx, db)
			return nil
		})
		assert.NoError(t, err)
	})

	t.Run("automatic rollback on returned error", func(t *testing.T) {
		expectedErr := errors.New("simulated error for rollback")
		err := uow.ExecuteInTx(ctx, func(txCtx context.Context) error {
			tx, ok := postgres.TxFromContext(txCtx)
			assert.True(t, ok)
			assert.NotNil(t, tx)
			return expectedErr
		})
		assert.ErrorIs(t, err, expectedErr)
	})

	t.Run("automatic rollback on panic", func(t *testing.T) {
		assert.Panics(t, func() {
			_ = uow.ExecuteInTx(ctx, func(txCtx context.Context) error {
				panic("simulated panic in transaction")
			})
		})
	})
}
