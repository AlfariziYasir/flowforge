# Implementation Plan — Go Generics, Squirrel SQL Builder & Unit of Work (TxManager)

Design and implement a generic base repository (`BaseRepository[T any]`) with **Masterminds/squirrel SQL Builder** and an atomic Unit of Work transaction manager (`UnitOfWork`) in `internal/platform/postgres/` for safe multi-tenant operations, automatic transaction commit/rollback, and zero manual `fmt.Sprintf` SQL string formatting using `pgx/v5`.

## User Review Required

> [!IMPORTANT]
> - `github.com/Masterminds/squirrel` is integrated as the SQL Builder for all database operations with `sq.StatementBuilder.PlaceholderFormat(sq.Dollar)`.
> - All manual `fmt.Sprintf` query builders have been completely removed.
> - `UnitOfWork` uses **Context-Based Transaction Injection** (`ExecuteInTx(ctx, fn)`), attaching `pgx.Tx` to `context.Context`. Repositories automatically detect if an active transaction exists in `ctx` or fall back to `pgxpool.Pool`.
> - `BaseRepository[T any]` uses Go Generics with `pgx.RowToStructByName[T]` and `pgx.CollectRows` for type-safe, reflection-less row scanning.
> - All base repository operations enforce mandatory `tenant_id` scoping to prevent cross-tenant data leaks.

## Proposed Changes

### Platform Postgres Abstractions (`internal/platform/postgres/`)

#### [MODIFY] [repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository.go)
- Refactored all queries (`FindByID`, `FindAll`, `Paginate`, `DeleteByPK`) to use `StatementBuilder` (`sq.StatementBuilder.PlaceholderFormat(sq.Dollar)`).
- Eliminated all manual `fmt.Sprintf` SQL construction.

#### [NEW] [dbtx.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/dbtx.go)
- Define `DBTX` interface matching `pgxpool.Pool` and `pgx.Tx` (`Exec`, `Query`, `QueryRow`).
- Implement context helpers: `ContextWithTx`, `TxFromContext`, and `GetDBTX`.

#### [NEW] [unit_of_work.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/unit_of_work.go)
- Define `UnitOfWork` interface and `pgxUnitOfWork` implementation.

---

## Verification Plan

### Automated Tests
- Run `go test -v -race ./...` to verify Squirrel SQL Builder queries and Unit of Work execution.
