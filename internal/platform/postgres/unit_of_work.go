package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UnitOfWork manages atomic database transactions via context injection.
type UnitOfWork interface {
	ExecuteInTx(ctx context.Context, fn func(ctx context.Context) error) error
	ExecuteInTxOptions(ctx context.Context, opts pgx.TxOptions, fn func(ctx context.Context) error) error
}

type pgxUnitOfWork struct {
	pool *pgxpool.Pool
}

// NewUnitOfWork returns a new UnitOfWork transaction manager backed by pgxpool.Pool.
func NewUnitOfWork(pool *pgxpool.Pool) UnitOfWork {
	return &pgxUnitOfWork{pool: pool}
}

// ExecuteInTx executes fn inside a default database transaction context.
func (u *pgxUnitOfWork) ExecuteInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return u.ExecuteInTxOptions(ctx, pgx.TxOptions{}, fn)
}

// ExecuteInTxOptions executes fn inside a database transaction context with custom options.
func (u *pgxUnitOfWork) ExecuteInTxOptions(ctx context.Context, opts pgx.TxOptions, fn func(ctx context.Context) error) error {
	if _, ok := TxFromContext(ctx); ok {
		return fn(ctx)
	}

	tx, err := u.pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	ctxWithTx := ContextWithTx(ctx, tx)

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
	}()

	if err := fn(ctxWithTx); err != nil {
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return fmt.Errorf("transaction execution error: %w (rollback error: %v)", err, rbErr)
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}
