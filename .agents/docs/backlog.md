# FlowForge — Implementation Backlog (Improved)

**Status:** Draft v2
**Purpose:** High-level implementation roadmap for FlowForge MVP.
**Note:** This backlog is intentionally higher-level than `task-specification.md`. It defines build order and milestone boundaries; detailed task execution will be handled later by the task specification standard.

---

## Guiding Rules

1. Build the core platform first.
2. Keep each milestone small enough to verify independently.
3. Prefer testable foundation work before UI polish.
4. Avoid mixing unrelated concerns in the same phase.
5. Use the frozen design documents as the source of truth:
   - `prd.md`
   - `architecture.md`
   - `database.md`
   - `api-1.md`
   - `api-2.md`
   - `api-3.md`
   - `api-4.md`
   - `ai-engineering-rules.md`
   - `task-specification.md`

---

## Phase 0 — Project Foundation

### Goal

Establish a stable engineering base before any feature work begins.

### Scope

- Repository structure
- Backend and frontend app bootstrap
- Config management
- Logging setup
- Error handling conventions
- Dependency injection pattern
- Local development environment
- Linting and formatting
- Test harness setup

### Done When

- The repo starts cleanly in local development.
- A basic backend health endpoint exists.
- A basic frontend starter page exists.
- Core conventions are documented and consistent.

---

## Phase 1 — Infrastructure and Local Runtime

### Goal

Make the full stack runnable locally.

### Scope

- Dockerfile(s)
- `docker-compose.yml`
- PostgreSQL container
- Redis container
- Backend migration runner
- Seed data strategy
- Local runtime configuration

### Done When

- `docker compose up` starts the full stack.
- Database migrations run successfully.
- The backend can connect to PostgreSQL and Redis.

---

## Phase 2 — Identity, Authentication, and Tenant Safety

### Goal

Create the security foundation for all tenant-scoped features.

### Scope

- Tenant model
- User model
- Role-based access control
- JWT authentication
- Auth middleware
- Tenant resolution from token
- Tenant-aware repository boundaries

### Done When

- Users can authenticate.
- Roles are enforced.
- Cross-tenant access is blocked.
- Authentication and tenant isolation are testable.

---

## Phase 3 — Workflow Domain Foundations

### Goal

Implement the core workflow ownership and versioning model.

### Scope

- Workflow metadata
- Workflow versioning model
- Draft and publish states
- Current version pointer
- Workflow node and edge storage
- Version rollback support

### Done When

- Workflows can be created, updated, published, and rolled back.
- Published workflow versions are immutable.
- The data model supports DAG-based workflow definitions.

---

## Phase 4 — Workflow Engine Core

### Goal

Build the deterministic execution engine for DAG workflows.

### Scope

- DAG validation (Kahn's Algorithm)
- Cycle detection
- Topological ordering
- Execution context resolution
- Node readiness calculation
- Step state transitions
- Expression evaluation engine (`github.com/expr-lang/expr`) for `TRANSFORM` and `CONDITION` nodes
- Retry logic
- Timeout logic
- Parallel branch execution rules

### Done When

- Invalid DAGs are rejected.
- Workflow execution order is deterministic.
- Expressions in `TRANSFORM` and `CONDITION` nodes evaluate safely in-memory.
- Failure handling behaves predictably.
- The engine is covered by unit tests.

---

## Phase 5 — Worker Runtime and Background Execution

### Goal

Run workflow executions asynchronously and safely.

### Scope

- Worker process entrypoint
- Queue consumption (Asynq / Redis)
- Execution coordinator layer
- Atomic run claim (`UPDATE ... WHERE status = 'pending' RETURNING id`)
- Atomic step claim
- Step executors (`HTTP`, `DELAY`, `CONDITION`, `TRANSFORM`)
- `SSRFValidator` implementation for `HTTP` step security (with dev/test whitelist)
- Bounded goroutine execution with `context.Context`
- Cancellation handling
- Crash recovery behavior
- Step executor `EVENT_PUBLISH` (publish ke message queue / gRPC) — lihat `design_event_driven_steps.md` kasus C
- **Observability**: counter (run dibuat/selesai/gagal, step per status, retry) dan histogram (durasi step, kedalaman antrian, umur run), semuanya berlabel `tenant_id`

### Done When

- A queued run is picked up by a worker.
- A run cannot be executed twice by concurrent workers.
- HTTP requests are validated against SSRF protection rules.
- Goroutine lifecycle is bounded and cancelable.
- Worker restart does not corrupt execution state.
- Antrian yang menumpuk, step yang retry berulang, dan run yang macet terlihat di metrik — bukan hanya dari laporan pengguna.

---

## Phase 6 — Workflow Execution API

### Goal

Expose lifecycle-based execution actions through the API.

### Scope

- Trigger workflow run
- List workflow runs
- Get run details
- Cancel run
- Retry run
- List step runs
- Get step details
- List execution logs
- AI analysis endpoint for failed runs
- Trigger dari message queue / gRPC (`trigger_type` = `queue` / `grpc`) — listener tipis yang memanggil use case pembuat run yang sama; lihat `design_event_driven_steps.md` kasus A

### Done When

- The API can create and inspect workflow executions.
- Run lifecycle actions behave according to the API contract.
- Execution endpoints are tenant-aware and idempotent where required.

---

## Phase 7 — Event-Driven Steps

### Goal

Memungkinkan sebuah workflow berhenti di tengah jalan untuk menunggu event eksternal, lalu melanjutkan saat event itu datang.

Desain lengkap: `.agents/plans/design_event_driven_steps.md` (kasus B).

### Scope

- Node type `EVENT_WAIT`
- Tabel `step_wait_tokens` — korelasi event ke run yang menunggu, `UNIQUE (tenant_id, correlation_key)`
- **Outbox** (`outbox_messages`) + relay publisher — jaminan pesan terkirim
- **Inbox** (`inbox_messages`) — jaminan pesan tidak diproses ganda
- Listener (consumer queue / gRPC server) yang membangunkan run
- Sweeper timeout untuk token kadaluarsa
- Dead-letter untuk event yatim + metrik `orphan_events_total`
- Batas jumlah run menunggu per tenant

### Done When

- Sebuah run bisa berstatus `waiting` tanpa menahan goroutine worker.
- Event yang datang membangunkan run yang benar, dan hanya sekali meski dikirim ulang.
- Pesan dan wait token ditulis atomik — worker crash tidak menghasilkan run yang menunggu pesan yang tak pernah terkirim.
- Event yang tidak cocok dengan token mana pun masuk dead-letter dan terlihat di metrik, tidak hilang diam-diam.
- Token yang kadaluarsa membawa run ke jalur error, bukan menggantung selamanya.

---

## Phase 8 — Real-Time Monitoring

### Goal

Show workflow execution progress live.

### Scope

- SSE endpoint (`GET /api/v1/events`)
- Event model
- Tenant-scoped Redis Pub/Sub channels (`events:tenant:{tenant_id}`)
- API in-memory `ClientManager` map (`map[tenantID]map[connectionID]chan Event`) for multi-instance routing
- Event publishing from worker execution
- Client reconnection support
- Heartbeat handling (30s)
- Live updates for runs, steps, and logs

### Done When

- The frontend receives live execution updates.
- Events stay tenant-scoped and do not leak across instances or tenants.
- SSE disconnects do not affect execution.

---

## Phase 9 — Workflow Builder Frontend

### Goal

Provide a usable UI for workflow creation and management.

### Scope

- App shell
- Login page
- Workflow list page
- Workflow detail page
- Workflow editor page
- Draft save flow
- Publish and rollback actions

### Done When

- A user can create and manage workflows from the UI.
- The UI reflects workflow version state correctly.

---

## Phase 10 — Execution Monitoring Frontend

### Goal

Provide a clear visual experience for workflow runs.

### Scope

- Run history page
- Run detail page
- Step state visualization
- Execution log viewer
- Live status updates
- Basic health dashboard

### Done When

- A user can inspect workflow runs and logs.
- The UI updates in real time during execution.
- The dashboard shows basic operational health.

---

## Phase 11 — AI Feature

### Goal

Add one meaningful AI-powered feature.

### Scope

- AI failure analysis for failed workflow runs (`POST /api/v1/workflow-runs/{runId}/analysis`)
- Redaction of sensitive headers/keys/payloads before calling LLM
- Strict JSON output schema validation (`diagnosis`, `possibleCause`, `suggestedFix`, `confidence`)
- Read-only UI presentation on run detail page

### Done When

- The AI failure analysis feature is visible in the run detail UI.
- Output is strictly validated against the JSON schema.
- Sensitive data is never sent to the model.

---

## Phase 12 — Testing and Reliability

### Goal

Prove the system behaves correctly under normal and concurrent use.

### Scope

- Unit tests for engine logic
- Repository tests for tenant filtering and persistence
- Integration tests for API flows
- End-to-end test for full workflow execution
- Race-focused tests for concurrency-sensitive code
- Smoke tests for startup and health checks

### Done When

- Core features are covered by tests.
- Concurrency-sensitive paths are validated.
- The system can be verified end to end.

---

## Phase 13 — CI, Documentation, and Portfolio Polish

### Goal

Make the repository presentable and easy to evaluate.

### Scope

- CI pipeline
- README
- Trade-offs section
- Architecture overview
- Review exercise file
- Demo data
- Demo screenshots or short recording

### Done When

- Pull requests are validated automatically.
- The repository is easy to understand.
- The project is ready to be shown as a portfolio piece.

---

## Milestone Freeze Criteria

Each phase is considered complete only when:

- its acceptance criteria are met,
- tests pass,
- and it no longer introduces design uncertainty.

Recommended freeze order:

1. Foundation
2. Identity and Tenant Safety
3. Workflow Domain
4. Workflow Engine
5. Worker Runtime
6. Execution API
7. Real-Time Monitoring
8. Frontend
9. AI Feature
10. Testing
11. Portfolio Polish

---

## Suggested Execution Order

For the fastest path to a believable MVP:

1. Foundation
2. Infrastructure
3. Identity and Tenant Safety
4. Workflow Domain
5. Workflow Engine Core
6. Worker Runtime
7. Execution API
8. Real-Time Monitoring
9. Frontend Workflow Builder
10. Frontend Monitoring
11. AI Feature
12. Testing and Reliability
13. CI and Portfolio Polish

---

## Minimum Demo Path

The smallest strong demo should show:

- login,
- create workflow,
- publish workflow version,
- trigger run,
- watch live execution,
- inspect run logs,
- and optionally generate or analyze using AI.

That path is enough to make the project feel like a simplified Zapier/n8n-style platform.
