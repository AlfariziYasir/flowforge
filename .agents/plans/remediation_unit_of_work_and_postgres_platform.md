# Problem Remediation Plan: Postgres Platform Issues

## Overview
This plan addresses security and code style findings discovered during the code review audit of the `internal/platform/postgres` package.

## Problems Encountered

### 1. [Medium Security Issue] Rollback Execution on Cancelled Context
- **Root Cause:** In `unit_of_work.go`, `tx.Rollback(ctx)` is invoked with the original caller context `ctx`. If the transaction failed because `ctx` was cancelled or timed out (e.g., HTTP request disconnect), `tx.Rollback(ctx)` attempts network I/O on a cancelled context, which fails and returns `context.Canceled`, masking the true error and risking lingering transaction locks.
- **Remediation:** Wrap the context passed to `tx.Rollback(...)` with `context.WithoutCancel(ctx)` (or `context.Background()`) to ensure rollback succeeds regardless of client context cancellation.

### 2. [Code Style Violation] `Paginate` Parameter Count
- **Root Cause:** `BaseRepository[T].Paginate(ctx, tenantID, page, pageSize, orderBy)` has 5 parameters, exceeding the recommended 4-parameter maximum for function signatures in Go style guidelines.
- **Remediation:** Introduce a struct `PaginationParams` to encapsulate `Page`, `PageSize`, and `OrderBy` when building high-level service and handler pagination layers.

## Proposed Changes

### [MODIFY] [unit_of_work.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/unit_of_work.go)
- Update `tx.Rollback(ctx)` calls to use `context.WithoutCancel(ctx)`.

### [MODIFY] [unit_of_work_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/unit_of_work_test.go)
- Add unit test verifying transaction rollback succeeds when `ctx` is explicitly cancelled prior to rollback execution.

## Verification Plan
1. Run `go test -v ./internal/platform/postgres/...` to verify all unit and context cancellation tests pass cleanly.
