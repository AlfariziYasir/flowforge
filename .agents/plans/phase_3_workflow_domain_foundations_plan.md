# Implementation Plan — Phase 3: Workflow Domain Foundations

Executing the architectural blueprint in [phase_3_workflow_domain_foundations.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_workflow_domain_foundations.md).

## Summary of Goals
- **Workflow Domain Models & Graph Representation**: Key-based logical graph representation and UUID-based persistence translation (`internal/domain/workflow.go`, `graph.go`).
- **Pure DAG Validation & Execution Engine**: `internal/engine/dag.go` with zero infrastructure dependencies, cycle detection, and deterministic topological ordering.
- **HTTP Envelope Standard (`httpx`)**: Unified API response envelope in `internal/platform/httpx/`, retrofitted onto existing `internal/auth` handlers.
- **Workflow Domain Services & UseCases**: CRUD, draft graph management, atomic publish/rollback with versioning and optimistic locking, audit logging (`internal/workflow/`).
- **Delivery Handlers & Route Wiring**: REST endpoints with RBAC middleware and input validation.

---

## Execution Phases

### Phase 1 — Domain Foundations & Engine
- [ ] `internal/domain/graph.go` & `graph_test.go` (Graph transformation & canonical sorting)
- [ ] `internal/domain/workflow.go` (Workflow models & status/node constants)
- [ ] `internal/engine/dag.go`, `dag_test.go`, & `purity_test.go` (DAG validation & deterministic Kahn's topological sort)

### Phase 2 — HTTP Platform Envelope & Auth Retrofit
- [ ] `internal/platform/httpx/httpx.go` & `httpx_test.go` (API response envelope)
- [ ] Retrofit `internal/auth/handler.go` & `middleware.go` to use `httpx`
- [ ] Update `internal/auth` & `cmd/api` test assertions for the standard HTTP envelope

### Phase 3 — Workflow Core Logic (DTOs, Repositories, UseCase, Audit)
- [ ] `internal/workflow/errors.go` & `dto.go`
- [ ] `internal/workflow/audit.go` (Audit logging within transaction boundaries)
- [ ] `internal/workflow/repository.go` & `repository_sql_test.go` (Pure SQL builders with optimistic locking)
- [ ] Update `.mockery.yaml` and generate mocks
- [ ] `internal/workflow/usecase.go` & `usecase_test.go` (Workflow management & version lifecycle)

### Phase 4 — Delivery, Wiring & CI Verification
- [ ] `internal/workflow/handler.go` & `handler_test.go`
- [ ] Wire routes & RBAC in `cmd/api/main.go`
- [ ] Run `make ci` (`fmt-check`, `vet`, `build`, `test -race -count=1`)
- [ ] Document in `.agents/memory/action_history.md`

---

## Verification Plan

### Automated Verification
- `go test ./internal/engine/... -race -count=1`
- `go test ./internal/domain/... -race -count=1`
- `go test ./internal/platform/httpx/... -race -count=1`
- `go test ./internal/auth/... -race -count=1`
- `go test ./internal/workflow/... -race -count=1`
- `make ci`
