# Implementation Plan — Phase 5: Worker Runtime and Background Execution

**Planner:** Claude (Architect) · **Executor:** Gemini Flash 3.6
**Source docs:** `backlog.md` §Phase 5 · `database_design.md` §7.7–7.9, §11.2 · `api-3.md` · `design_event_driven_steps.md` case C · `AGENTS.md`
**Predecessor:** Phase 4 verified in-tree — `internal/engine/{dag,scope,evaluator,state,readiness}.go` all present, `make ci` **PASS**, migration `000002` applied to the repo.

---

## 0. Verification Log

| Claim | Result |
|---|---|
| Phase 4 code-complete | ✅ all five engine files + tests present; `make ci` PASS |
| `migration 000002` shipped narrowed (branch + `step_runs.waiting` only) | ✅ confirmed — `node_type` **not** widened, exactly as D-6 v3 decided |
| Engine API available to Phase 5 | ✅ `CalculateReadyNodes(g, states, Scope)`, `EvaluateCondition/Transform(ctx, expr, Scope)`, `Interpolate`, `RetryPolicy.NextBackoff`, `TimeoutPolicy.EffectiveTimeout`, `IsTerminal`, `CanTransition` |
| `cmd/worker/main.go` is a skeleton | ✅ signal handling only; carries `TODO(Phase 5)` for the fixed 100 ms drain sleep |
| `internal/platform/queue/` exists | ✅ empty directory — intent already staked out |
| Asynq usable | ✅ `v0.26.0`; depends on `go-redis/v9 v9.14.1`, project is on `v9.21.0` — same major, no conflict |
| Prometheus client usable | ✅ `client_golang v1.24.1` |
| Run/step repositories exist | ❌ **none** — only `internal/engine/state.go` references those tables, as constants |
| SSRF allowlist config exists | ⚠️ `AllowedHTTP` exists — **and its default is dangerous**, see B-1 |

---

## 1. Task Summary

**Goal.** Execute workflow runs asynchronously: consume a queue, claim work atomically so no run executes twice, drive the Phase 4 engine over a run's graph, execute the five node types with bounded concurrency and cancellation, survive worker crashes, and expose it all in metrics.

**Phase 4 gave us pure decision-making. Phase 5 supplies the I/O and the clock.** Nothing in `internal/engine` changes — if a Phase 5 requirement seems to need an engine edit, that is a signal the logic belongs in the coordinator instead.

**Out of scope, with owner:** HTTP endpoints for runs (Phase 6) · queue/gRPC *triggers* (Phase 6) · `EVENT_WAIT` and wait tokens (Phase 7) · AI failure analysis (Phase 11).

---

## 2. The Blocking Prerequisite — integration testing stops being deferrable

Every previous phase could be proven with mocks. **Phase 5 cannot**, and this is the defining constraint on the whole plan.

Its headline acceptance criterion is *"a run cannot be executed twice by concurrent workers."* That property lives entirely in PostgreSQL's row-locking semantics. A mock returning "1 row affected" proves nothing: it is the assertion under test, restated. The same applies to `FOR UPDATE SKIP LOCKED`, to crash recovery mid-transaction, and to the `step_runs` CHECK constraint that Phase 4's D-4 was written to satisfy.

Meanwhile: **no migration has ever been applied to any database.** `getTestPool` skips unless `FLOWFORGE_INTEGRATION=1`, so `000001` and `000002` have never executed. `000002`'s `DROP CONSTRAINT IF EXISTS step_runs_status_check` guesses a PostgreSQL auto-generated name that has never been observed.

> [!IMPORTANT]
> **Step 0 of this phase is `make up && make migrate && make test-integration` on a real Postgres and Redis.** Not as a checkpoint at the end — as the *first* task, before a line of worker code. Two migrations, roughly 20 tables and constraints, and three phases of repository SQL are about to run for the first time. Discovering a broken migration while also debugging a concurrent claim is a bad trade.

Concretely, step 0 must confirm: both migrations apply cleanly; the `000002` down-migration reverses them; the auto-generated constraint name assumption held; and the existing Phase 3 workflow repository tests pass against a real database with `FLOWFORGE_INTEGRATION=1`.

---

## 3. Resolved Decisions

| # | Decision | Rationale |
|---|---|---|
| **D-1** | **Phase 5 owns run + step persistence**, not just the worker | Backlog assigns Phase 5 the atomic run claim and atomic step claim. A claim needs rows to claim. Phase 6 adds the HTTP surface *on top of* the use case Phase 5 builds — it does not introduce the tables. |
| **D-2** | Queue = **Asynq v0.26.0** | `backlog.md` names it; verified compatible with the project's `go-redis/v9`. It supplies retry, scheduling, and a shutdown handshake that would otherwise be hand-rolled — and `cmd/worker/main.go` already carries a TODO anticipating it. |
| **D-3** | The queue carries **only a run ID + tenant ID**, never the graph or payload | Keeps messages small, avoids a stale snapshot if the run is cancelled mid-flight, and means Redis never holds tenant data. The worker re-reads authoritative state from Postgres on pickup. |
| **D-4** | **Postgres is the source of truth for claims; Redis only wakes workers** | If Asynq and Postgres ever disagree, Postgres wins. A duplicate queue delivery must be harmless because the DB claim fails, not because the queue promised exactly-once — no queue does. |
| **D-5** | `EVENT_PUBLISH` needs **migration `000003`** widening `workflow_nodes.node_type` | The narrowed D-6 from Phase 4 deliberately left this to the phase that introduces the value. This is that phase. |
| **D-6** | Executors live in `internal/execution/`, **never** in `internal/engine/` | Engine purity is mechanically enforced (T-26/P-22). Executors do HTTP and DB work by definition. |
| **D-7** | Crash recovery via **lease + heartbeat**, not visibility timeout | See §6. |
| **D-8** | Metrics: `tenant_id` label **only on counters, with a cardinality ceiling** | Backlog asks for `tenant_id` on everything. On histograms that multiplies series by bucket count — see B-2. |

---

## 4. Two Hazards Found While Planning

### B-1 (SECURITY) — the SSRF allowlist default defeats SSRF protection

[config.go:31](../../internal/platform/config/config.go#L31):

```go
AllowedHTTP: getEnv("ALLOWED_HTTP_HOSTS", "httpbin.org,localhost,127.0.0.1"),
```

The default allows `localhost` and `127.0.0.1` — precisely the targets SSRF protection exists to block. A tenant publishing an `HTTP` node pointed at `http://127.0.0.1:5432` or the cloud metadata endpoint would be *permitted by default*, and the field name reads like a safety feature, which makes it worse.

**Fix, as part of this phase:**

- The allowlist is **not** the security boundary. The boundary is a **denylist applied after DNS resolution**: reject loopback, link-local (`169.254.0.0/16`, including the `169.254.169.254` metadata address), private ranges (RFC 1918), unique-local IPv6, and unspecified/multicast.
- Rename to `ALLOWED_HTTP_HOSTS` = an *additional* opt-in for development only, and **ignore it entirely** when `isProductionLike(cfg.Environment)` — same pattern `main.go` already uses for the JWT secret.
- Default it to **empty**, not to loopback.
- Validate **after resolution and on every redirect hop**, and pin the dialled IP to the one validated. Checking the hostname before `http.Client` resolves it is a DNS-rebinding hole.

### B-2 (OPERABILITY) — `tenant_id` on histograms is a cardinality bomb

Backlog: *"counter … dan histogram (durasi step, kedalaman antrian, umur run), semuanya berlabel `tenant_id`."*

A histogram with 10 buckets × 4 node types × N tenants is 40N time series from one metric. At 500 tenants that is 20,000 series for `step_duration` alone, and tenant IDs are UUIDs — unbounded and never garbage-collected by Prometheus.

**Recommendation:** counters carry `tenant_id` (cheap, one series each); histograms carry `node_type` and `status` but **not** `tenant_id`. Per-tenant latency, when genuinely needed, comes from the `execution_logs` table, which is already tenant-scoped and indexed. If per-tenant histograms are non-negotiable, cap them with an explicit allowlist of monitored tenants and document the ceiling.

This is a deviation from the backlog wording, so it is called out rather than quietly applied.

---

## 5. Architecture

```
internal/execution/          ← new: coordinator, repositories, executors (does I/O)
    repository.go            RunRepository, StepRunRepository, LogRepository
    coordinator.go           drives internal/engine over one run
    usecase.go               CreateRun / CancelRun — Phase 6's handlers call these
    executor/
        executor.go          Executor interface + registry
        http.go              HTTP node (uses SSRFValidator)
        delay.go             DELAY node
        condition.go         CONDITION node (engine.EvaluateCondition)
        transform.go         TRANSFORM node (engine.EvaluateTransform)
        event_publish.go     EVENT_PUBLISH node (D-5)
internal/platform/queue/     ← directory already exists, empty
    asynq.go                 Client (enqueue) + Server (consume) wrappers
internal/platform/safehttp/
    ssrf.go                  SSRFValidator + a pinned-IP http.Transport (B-1)
internal/platform/metrics/
    metrics.go               Prometheus collectors (B-2)
```

### 5.1 Domain — `internal/domain/execution.go`

`WorkflowRun`, `StepRun`, `ExecutionLog`, tagged against `migrations/000001` §7.7–7.9. Nullable columns (`started_at`, `finished_at`, `error_payload`, `idempotency_key`, `step_run_id`) **must** be pointers — the Phase 3 lesson.

Run statuses mirror the CHECK exactly, same discipline as Phase 4's D-4: `pending`, `running`, `succeeded`, `failed`, `canceled`, `timed_out`. **Note `canceled`, one `l`** — it is what `migration:119` says, and a Go constant spelled `cancelled` would pass every unit test and fail at the first insert.

### 5.2 Atomic claims (`backlog.md`, `database_design.md` §11.2)

**Run claim** — one statement, no read-then-write:

```sql
UPDATE workflow_runs
   SET status = 'running', started_at = NOW(), updated_at = NOW(),
       claimed_by = $2, lease_expires_at = NOW() + $3::interval
 WHERE id = $1 AND tenant_id = $4 AND status = 'pending'
RETURNING id, workflow_id, workflow_version_id;
```

Zero rows returned is the **normal** outcome of a duplicate delivery, not an error. The worker acks and moves on. Logging it at `error` would make a healthy system look sick.

**Step claim** — `FOR UPDATE SKIP LOCKED` when several workers may pull steps of the same run:

```sql
SELECT id FROM step_runs
 WHERE tenant_id = $1 AND workflow_run_id = $2 AND status = 'ready'
 ORDER BY node_key
   FOR UPDATE SKIP LOCKED
 LIMIT $3;
```

`claimed_by` and `lease_expires_at` are new columns → **migration `000003`**, alongside D-5's `node_type` widening.

### 5.3 Coordinator

Pure orchestration; every decision delegates to `internal/engine`.

```go
// Tick advances one run by one scheduling round: load state, ask the engine what
// is ready, claim and dispatch those steps, persist results, then decide whether
// the run is finished. Returns whether more work remains.
func (c *Coordinator) Tick(ctx context.Context, tenantID, runID uuid.UUID) (more bool, err error)
```

Per tick: load run + version graph → build `engine.Scope` from `input_context` and completed `step_runs` → `engine.CalculateReadyNodes` → persist `skipped` steps → claim and execute ready steps under a bounded `errgroup` → write outputs → terminal check.

**Two rules the coordinator must not break:**

1. **`CanTransition` gates every status write.** The state machine exists to be used; a direct `UPDATE ... SET status` bypassing it is how illegal transitions reach the database.
2. **The scope is rebuilt from persisted step outputs each tick**, never carried in memory across ticks. A worker that crashes mid-run must be resumable by a different worker with no shared state.

### 5.4 Executors

```go
type Executor interface {
    Type() string
    Execute(ctx context.Context, in Input) (Output, error)
}
```

`ctx` always carries `engine.TimeoutPolicy.EffectiveTimeout(node)`, applied by the coordinator — not by each executor, so no executor can forget it. `HTTP` additionally passes it to the request.

- **HTTP** — `Interpolate` the URL/headers/body from scope, validate via `SSRFValidator` (B-1), cap the response body with `io.LimitReader`, and record status + parsed body as output. An oversized response is a step failure, not an OOM.
- **DELAY** — `select` on `time.After` and `ctx.Done()`. Never a bare `time.Sleep`: it ignores cancellation, and "cancellation handling" is an acceptance criterion.
- **CONDITION** — `engine.EvaluateCondition`, result written to `output_payload.result` where `engine.CalculateReadyNodes` expects it (`readiness.go:149 branchTaken`).
- **TRANSFORM** — `engine.EvaluateTransform`.
- **EVENT_PUBLISH** — publish to the queue; `design_event_driven_steps.md` case C says it mirrors HTTP in shape.

### 5.5 Bounded concurrency and shutdown

`golang.org/x/sync/errgroup` with `SetLimit(cfg.WorkerConcurrency)`. New config: `WORKER_CONCURRENCY` (default 10), `WORKER_QUEUES`, `RUN_LEASE_DURATION` (default 60s), `STEP_MAX_BODY_BYTES` (default 1 MiB).

`cmd/worker/main.go` replaces its `time.Sleep(100 * time.Millisecond)` — the TODO already flags it — with `asynq.Server.Shutdown()`, which drains in-flight tasks. **Every goroutine must be owned by an `errgroup` or `WaitGroup`.** A goroutine leak here is invisible in tests and fatal in production.

---

## 6. Crash Recovery (D-7)

Asynq's own retry covers a worker dying *before* it claims. It does **not** cover a worker dying *after* claiming, when the run sits in `running` with no one advancing it.

**Lease + heartbeat.** The claim sets `lease_expires_at = NOW() + interval`; a background ticker extends it while work is in flight; a reaper reclaims runs whose lease has expired:

```sql
UPDATE workflow_runs
   SET status = 'pending', claimed_by = NULL, lease_expires_at = NULL
 WHERE status = 'running' AND lease_expires_at < NOW()
RETURNING id, tenant_id;
```

Chosen over a queue visibility timeout because the timeout would have to exceed the longest possible run (`MaxStepTimeout` is 15 minutes × step count), which makes recovery from a fast crash unacceptably slow.

**The half-executed step is the real question.** A step may have completed its side effect — an HTTP POST that charged a card — before the crash. Phase 5 takes the honest position: **steps are at-least-once, and that is documented, not hidden.** A reclaimed run resumes from persisted step state; a step recorded `running` at reclaim time is retried, and `attempt_count` makes the retry visible. True exactly-once needs idempotency keys on the executor side, which `api-3.md` places in Phase 6.

---

## 7. Boundary Checklist

- [ ] `internal/engine/` unchanged — purity test still passes untouched.
- [ ] `internal/execution/` never imports `internal/auth` (tenant ID arrives as a parameter).
- [ ] Every repository query filters `tenant_id`.
- [ ] Every status write goes through `engine.CanTransition`.
- [ ] Every goroutine is owned by an `errgroup`/`WaitGroup`; none outlives `Shutdown`.
- [ ] Every executor honours `ctx` cancellation; no bare `time.Sleep`.
- [ ] SSRF validation happens **after** DNS resolution and on **every** redirect hop.
- [ ] `ALLOWED_HTTP_HOSTS` is ignored in production-like environments.
- [ ] Run/step status constants are provably subsets of their CHECK constraints.
- [ ] No tenant data is written to Redis (D-3).

---

## 8. TDD Specification

testify · table-driven · `-race`. **Split by what each layer can actually prove.**

### 8.1 Unit — mocked repositories

- **Q-1** Coordinator drives a linear graph to `succeeded`, marking each step in order.
- **Q-2** A CONDITION false skips the false branch transitively (coordinator honours `CalculateReadyNodes`).
- **Q-3** A failed step with attempts remaining schedules a retry with `NextBackoff`; exhausted attempts fail the run.
- **Q-4** ★ An illegal transition is rejected — force `succeeded → running` and assert no repository write occurred.
- **Q-5** Cancelled ctx mid-tick stops dispatch and leaves no goroutine running (`goleak` or an explicit `WaitGroup` assertion).
- **Q-6** Scope is rebuilt from persisted step outputs, not carried across ticks — run two ticks against a repository that returns different data and assert the second tick reflects it.
- **Q-7** Zero-row run claim (duplicate delivery) is a no-op with no error and no `error`-level log.

### 8.2 Unit — executors

- **Q-8** DELAY returns promptly on cancellation, not after its full duration.
- **Q-9** HTTP interpolates URL/headers/body from scope; an unresolvable placeholder fails the step.
- **Q-10** ★ HTTP response body over `STEP_MAX_BODY_BYTES` fails the step and does not buffer the whole body.
- **Q-11** CONDITION writes its boolean to `output_payload.result` — the exact key `readiness.branchTaken` reads. *(An integration point that a mock on either side would hide.)*
- **Q-12** Executor honours the coordinator-applied timeout.

### 8.3 Unit — SSRF (B-1)

- **Q-13** ★ Blocks `127.0.0.1`, `localhost`, `169.254.169.254`, `10.0.0.1`, `192.168.1.1`, `172.16.0.1`, `[::1]`, `[fc00::1]`, and `0.0.0.0`.
- **Q-14** ★ Blocks a hostname that **resolves** to a private IP — validation after resolution, not on the string.
- **Q-15** ★ Blocks a redirect from a public host to `127.0.0.1`. The most commonly missed SSRF hole.
- **Q-16** `ALLOWED_HTTP_HOSTS` is honoured in development and **ignored** when `ENV=production`.
- **Q-17** Permits an ordinary public address.

### 8.4 Integration — `FLOWFORGE_INTEGRATION=1`, real Postgres (§2)

**These cannot be mocked. They are the phase's actual acceptance criteria.**

- **Q-18** ★★ **Concurrent claim.** N goroutines claim the same `pending` run; **exactly one** succeeds. Run with `-race`, repeat ≥50 times.
- **Q-19** ★★ **Concurrent step claim.** Two workers pull ready steps of one run under `SKIP LOCKED`; no step is claimed twice and none is starved.
- **Q-20** ★ Every Go status constant is accepted by its CHECK — insert each one. Proves Phase 4's D-4 against the database that motivated it.
- **Q-21** ★ Reaper reclaims an expired lease; a fresh worker resumes and completes the run.
- **Q-22** ★ Migrations `000001`+`000002`+`000003` apply cleanly from scratch, and each down-migration reverses — including the `000002` auto-generated-constraint-name assumption (§2).
- **Q-23** A run cancelled mid-flight stops dispatching new steps and lands in `canceled`.

### 8.5 Metrics

- **Q-24** Counters carry `tenant_id`; histograms **do not** (B-2). Assert the label sets explicitly so a later "helpful" addition trips a test.

---

## 9. Execution Order

**Step 0 is not optional and not reorderable** (§2).

0. `make up` · apply `000001` + `000002` · `FLOWFORGE_INTEGRATION=1 make test-integration`. Fix whatever this uncovers **before** writing Phase 5 code. Record the findings — this is the first real exercise of three phases of SQL.
1. Migration `000003`: `claimed_by`, `lease_expires_at`, `node_type += EVENT_PUBLISH`, plus `.down.sql`. Verify with Q-22.
2. `internal/domain/execution.go` — structs and status constants, tags checked column-by-column. Q-20 immediately, against the real DB.
3. `internal/platform/safehttp/ssrf.go` (Q-13…Q-17). **Early and standalone** — it is the security boundary and has no dependencies.
4. `internal/execution/repository.go` — claims first (Q-18, Q-19 as integration tests before the coordinator exists to complicate them).
5. `internal/platform/queue/asynq.go` — client + server wrappers.
6. `internal/execution/executor/*` (Q-8…Q-12).
7. `internal/execution/coordinator.go` (Q-1…Q-7).
8. `internal/platform/metrics/` (Q-24).
9. `cmd/worker/main.go` — real wiring, `asynq.Server.Shutdown()`, delete the `time.Sleep` TODO.
10. `internal/execution/usecase.go` — `CreateRun`/`CancelRun`, the seam Phase 6 will call.
11. `make ci` **and** `FLOWFORGE_INTEGRATION=1 make test-integration`. Both green.
12. Append a Phase 5 entry to `.agents/memory/action_history.md`.

**Reporting rule.** Runtime figures from `-race` only. Concurrency tests report iteration counts, not a single pass — one green run of a race test proves very little.

---

## 10. Definition of Done

- [ ] `make up` brings up the stack and all migrations apply cleanly (§2).
- [ ] A queued run is picked up and executed to a terminal status.
- [ ] ★ A run cannot be executed twice by concurrent workers — proven against real Postgres, ≥50 iterations under `-race` (Q-18).
- [ ] ★ Steps cannot be double-claimed (Q-19).
- [ ] SSRF blocks loopback, link-local, private ranges, DNS-resolved private addresses, and redirect hops (Q-13…Q-15).
- [ ] `ALLOWED_HTTP_HOSTS` cannot weaken production (Q-16).
- [ ] Goroutines are bounded, cancellable, and drained on shutdown (Q-5).
- [ ] An expired lease is reclaimed and the run resumes (Q-21).
- [ ] Every status constant is accepted by its CHECK (Q-20).
- [ ] Metrics label sets are pinned (Q-24).
- [ ] `make ci` green **and** integration suite green with `FLOWFORGE_INTEGRATION=1`.

---

## 11. Carried Forward

1. **V-1** — `ContextWithTx`/`TxFromContext` widened to `DBTX` ([phase_3_close_out_review.md](phase_3_close_out_review.md)). Still open; Phase 5 adds repositories on top, so landing it first avoids a second retrofit.
2. **PostgreSQL 15+ floor** recorded only in migration comments.
3. **At-least-once step execution** is a documented property (§6), not a bug. Exactly-once needs executor-side idempotency keys — `api-3.md` puts those in Phase 6.
4. **B-2's backlog deviation** — histograms without `tenant_id`. Needs the user's acknowledgement, since it contradicts the backlog wording.
