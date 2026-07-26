# Project Context: FlowForge (Workflow Automation Engine)

## 1. Domain & Architecture Specs
- **Project Name**: FlowForge (Multi-tenant Workflow Automation Platform)
- **Architecture**: Modular Monolith with split runtimes (`cmd/api` and `cmd/worker`)
- **Language**: Go (Golang) `v1.22+`
- **Primary Database**: PostgreSQL (Source of truth for all workflow, execution, & log state via `pgx/v5` / SQL)
- **Queue & PubSub**: Redis + Asynq (Operational support for execution jobs & SSE event fan-out)
- **Testing Framework**: Native `testing` + `stretchr/testify` (assert/require)
- **Expression Engine**: `github.com/expr-lang/expr` (Sandboxed, pure in-memory evaluation)

## 2. Strict Boundary Rules (`backend/internal/`)
- `engine/`: PURE DOMAIN LOGIC ONLY. Responsible for DAG validation (Kahn's Algorithm), cycle detection, and topological sorting. **MUST NOT** import HTTP frameworks, Redis, or DB drivers.
- `executor/`: Dedicated step executors (`HTTP`, `DELAY`, `CONDITION`, `TRANSFORM`).
- `run/`: Owns workflow run and step run persistence layer.
- `platform/`: Adapters (PostgreSQL, Redis, Queue, Config).
- **Multi-Tenancy Isolation**: Every DB query and Redis channel (`events:tenant:{tenant_id}`) MUST enforce `tenant_id` scoping without exception.

## 3. Concurrency, Security & Execution Guardrails
- **Worker Concurrency**: Bounded Goroutine Pools with explicit ownership. Every goroutine MUST accept and respect `context.Context` cancellation/deadlines.
- **Atomic Claims**: Worker MUST claim runs/steps using atomic DB operations (`UPDATE ... WHERE status = 'pending' RETURNING id` or `FOR UPDATE SKIP LOCKED`).
- **SSRF Protection**: HTTP Executor MUST validate destination IP ranges against `SSRFValidator` (block `localhost`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `169.254.169.254`).
- **Immutability**: Published `workflow_versions` and finished `workflow_runs` are strictly IMMUTABLE.

## 4. Coding & Error Standards (`golang-*` skills)
- **Error Handling**: Use Sentinel Errors (`var ErrCycleDetected = errors.New(...)`). Always wrap external errors with `fmt.Errorf("context: %w", err)`. **NO PANIC**.
- **Testing (`/tdd`)**:
  - Follow strict Red-Green-Refactor cycles.
  - Mandatory **Table-Driven Tests** for DAG validation, Kahn's algorithm, and executors.
  - Tests must cover context cancellation and goroutine leak prevention.
- **Code Style**: Idiomatic Go, Linter-compliant (`golangci-lint`), small interfaces (*Deep Module principle*).

## 5. Skill Integration Protocols
When interacting with Gemini using skills, adhere to these behaviors:

1. **`/tdd`**: Write failing unit tests first in `internal/engine/` or relevant packages before implementation. Run `go test -v ./...`.
2. **`/code-review`**: Audit `git diff` against `golang-security` (SSRF, SQL injection, secrets), `golang-code-style`, and tenant isolation rules in this file.
3. **`/grill-me`**: Before implementing engine/worker logic, challenge the design regarding race conditions, atomic claim edge cases, and context timeouts.
4. **`/improve-codebase-architecture`**: Assess if modules (e.g., `engine` vs `executor`) maintain strict boundary separation and deep interfaces.

---
*Single Source of Truth for FlowForge Engine Development*