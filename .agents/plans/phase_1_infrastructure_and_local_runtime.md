# Phase 1 Implementation Plan — Infrastructure and Local Runtime

Build the infrastructure foundation and local runtime environment for FlowForge, making the full stack (`cmd/api`, `cmd/worker`, PostgreSQL 17, Redis, and DB migrations) runnable locally via Docker Compose with verified database and Redis health checks.

## User Review Required

> [!IMPORTANT]
> - Database schema includes full DDL for all 11 core tables specified in `database_design.md` (`tenants`, `users`, `workflows`, `workflow_versions`, `workflow_nodes`, `workflow_edges`, `workflow_runs`, `step_runs`, `execution_logs`, `audit_logs`, `idempotency_keys`).
> - Migrations are run via `golang-migrate/migrate` CLI service in `docker-compose.yml`.
> - `/health` endpoint in `cmd/api` is upgraded to perform active pings to PostgreSQL pool and Redis.

## Proposed Changes

### Platform Infrastructure & Adapters

#### [NEW] [postgres.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/postgres.go)
- Create PostgreSQL connection pool manager using `github.com/jackc/pgx/v5/pgxpool`.
- Provide `NewPool(ctx, cfg)` and `Ping(ctx)` functions.

#### [NEW] [redis.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/redis/redis.go)
- Create Redis client helper using `github.com/redis/go-redis/v9`.
- Provide `NewClient(cfg)` and `Ping(ctx)` functions.

---

### Database Migrations & Seed Data

#### [NEW] [000001_init_schema.up.sql](file:///home/mohyasiralfarizi/Golang/flowforge/migrations/000001_init_schema.up.sql)
- Complete DDL definitions for:
  - `tenants`
  - `users`
  - `workflows`
  - `workflow_versions`
  - `workflow_nodes`
  - `workflow_edges`
  - `workflow_runs`
  - `step_runs`
  - `execution_logs`
  - `audit_logs`
  - `idempotency_keys`
- Foreign keys, check constraints, unique indexes, and tenant-first composite indexes.

#### [NEW] [000001_init_schema.down.sql](file:///home/mohyasiralfarizi/Golang/flowforge/migrations/000001_init_schema.down.sql)
- Clean `DROP TABLE IF EXISTS` operations in reverse dependency order.

#### [NEW] [seed.sql](file:///home/mohyasiralfarizi/Golang/flowforge/migrations/seed.sql)
- Seed default workspace tenant (e.g. `default-tenant`) and default admin user with hashed password for local development.

---

### Docker & Local Runtime Configuration

#### [NEW] [Dockerfile.api](file:///home/mohyasiralfarizi/Golang/flowforge/Dockerfile.api)
- Multi-stage Dockerfile for Go API service.

#### [NEW] [Dockerfile.worker](file:///home/mohyasiralfarizi/Golang/flowforge/Dockerfile.worker)
- Multi-stage Dockerfile for Go Worker service.

#### [NEW] [docker-compose.yml](file:///home/mohyasiralfarizi/Golang/flowforge/docker-compose.yml)
- Services:
  - `postgres`: PostgreSQL 17 container with healthcheck (`pg_isready`).
  - `redis`: Redis 7 container with healthcheck (`redis-cli ping`).
  - `migrate`: Runs `golang-migrate` against `postgres` after it is healthy.
  - `api`: Builds `cmd/api` and connects to `postgres` and `redis`.
  - `worker`: Builds `cmd/worker` and connects to `postgres` and `redis`.

---

### API Health Endpoint Enhancement

#### [MODIFY] [main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)
- Initialize PostgreSQL pool and Redis client on startup.
- Update `/health` handler to query DB pool `Ping(ctx)` and Redis `Ping(ctx)` and return structured status (`200 OK` if healthy, `503 Service Unavailable` if unhealthy).

#### [MODIFY] [main_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main_test.go)
- Unit tests for `/health` endpoint handler.

---

## Verification Plan

### Automated Tests
- Run `go test -v ./...` to verify platform package unit tests and health check handlers.
- Run `go test -race ./...` to ensure zero data races.

### Manual Verification
- Execute `docker compose up --build -d` to bring up the full local stack.
- Check migration container logs to ensure `000001_init_schema.up.sql` executed cleanly.
- Perform HTTP GET request to `http://localhost:8080/health` and verify `200 OK` response with DB and Redis `status: "up"`.
- Run `docker compose down -v` to confirm clean teardown.
