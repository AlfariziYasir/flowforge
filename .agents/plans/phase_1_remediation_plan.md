# Problem Remediation Plan: Phase 1 Infrastructure Issues

## Overview
During audit of the Phase 1 (Infrastructure and Local Runtime) implementation, three key configuration and runtime issues were identified.

## Problems Encountered

### 1. [Critical] `docker-compose` Seed Execution Race Condition
- **Root Cause:** `docker-compose.yml` mounts `./migrations/seed.sql` to `/docker-entrypoint-initdb.d/99_seed.sql:ro` on the `postgres` service. When PostgreSQL initializes for the first time, entrypoint scripts in `/docker-entrypoint-initdb.d/` execute immediately—**before** schema migrations (`golang-migrate`) run. Because `tenants` and `users` tables do not exist yet, PostgreSQL initialization fails with `ERROR: relation "tenants" does not exist`.
- **Remediation Plan:** 
  1. Remove `/docker-entrypoint-initdb.d/99_seed.sql` volume mount from `postgres` container in `docker-compose.yml`.
  2. Option A: Create a database seed runner container `seed` in `docker-compose.yml` that runs `psql` or a Go seed runner after `migrate` completes (`depends_on: migrate: condition: service_completed_successfully`).
  3. Option B: Create a numbered SQL migration file (e.g. `000002_seed_dev_data.up.sql`) or dedicated migration seed step.

### 2. [Medium] Missing Container Healthcheck for `api` Service
- **Root Cause:** `Dockerfile.api` installs `curl` specifically to enable container health checks, but `docker-compose.yml` lacks a `healthcheck` block for the `api` service.
- **Remediation Plan:** Add a standard Docker `healthcheck` to `api` in `docker-compose.yml`:
  ```yaml
  healthcheck:
    test: ["CMD-SHELL", "curl -f http://localhost:8080/health || exit 1"]
    interval: 5s
    timeout: 5s
    retries: 5
  ```

### 3. [Low] Deferred Resource Cleanup on `os.Exit(1)` in `cmd/api/main.go`
- **Root Cause:** Connection pool closes (`dbPool.Close()`, `rClient.Close()`) are deferred in `main()`. If `server.ListenAndServe()` fails and calls `os.Exit(1)` inside its goroutine, `main()` defers are bypassed.
- **Remediation Plan:** Refactor server error channel handling to gracefully return from `main()` or explicitly close connection pools before calling `os.Exit(1)`.

## Proposed Changes

### [MODIFY] [docker-compose.yml](file:///home/mohyasiralfarizi/Golang/flowforge/docker-compose.yml)
- Remove `/docker-entrypoint-initdb.d/99_seed.sql` volume mount from `postgres`.
- Add `seed` container service dependent on `migrate` completing successfully.
- Add `healthcheck` block to `api` service.

### [MODIFY] [cmd/api/main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)
- Handle server startup errors without bypassing deferred pool cleanup.

## Verification Plan
1. Run `docker compose up --build` on a clean volume (`docker compose down -v`) to confirm PostgreSQL initializes, migrations execute, and seed data populates without `relation does not exist` errors.
2. Run `docker compose ps` to verify all services (`postgres`, `redis`, `api`, `worker`) become healthy.
3. Test `curl http://localhost:8080/health` returning `200 OK`.
