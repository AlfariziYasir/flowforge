package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX defines the database operations interface satisfied by both *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type txKey struct{}
type dbtxKey struct{}

// ContextWithTx injects an active pgx.Tx into the given context.
func ContextWithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// TxFromContext retrieves a pgx.Tx from context if present.
func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}

// ContextWithDBTX injects a DBTX runner override into the given context (for test assertions).
func ContextWithDBTX(ctx context.Context, db DBTX) context.Context {
	return context.WithValue(ctx, dbtxKey{}, db)
}

// DBTXFromContext retrieves a DBTX runner override from context if present.
func DBTXFromContext(ctx context.Context) (DBTX, bool) {
	db, ok := ctx.Value(dbtxKey{}).(DBTX)
	return db, ok
}

// GetDBTX returns the active transaction from context if available, otherwise returning pool.
func GetDBTX(ctx context.Context, pool *pgxpool.Pool) DBTX {
	if tx, ok := TxFromContext(ctx); ok {
		return tx
	}
	if db, ok := DBTXFromContext(ctx); ok {
		return db
	}
	return pool
}
