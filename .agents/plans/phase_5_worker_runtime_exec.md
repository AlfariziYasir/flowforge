# Execution Record — Phase 5: Worker Runtime and Background Execution

Plan: [phase_5_worker_runtime.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_5_worker_runtime.md). **Status: DONE — both suites green, E2E through real Redis verified.**

## Step 0 — Integration prerequisite (real Postgres + Redis)

- `make up` on this host needed two infra fixes: SELinux Enforcing required `:z` on the compose bind mounts (`migrations`, `seed.sql`); podman short-name resolution needed pre-pulled image names.
- Migrations `000001` + `000002` applied cleanly for the first time ever. **The `000002` auto-generated-constraint assumption held** — `step_runs.status` CHECK was recreated as `chk_step_runs_status` with `waiting`.
- Integration suite surfaced **two pre-existing test defects**, both fixed:
  - `TestBaseRepository_Integration/Create_Success` inserted `Role: "user"` — violates `users_role_check` (only `admin/editor/viewer`).
  - The same test was non-idempotent (fixed `(tenant_id, email)` collided on re-run) — now pre-clears its row.

## What was built

| Area | Files | Notes |
|---|---|---|
| Migration 000003 | `migrations/000003_worker_claims.{up,down}.sql` | `claimed_by` (varchar), `lease_expires_at` (timestamptz), reaper partial index, `node_type` += `EVENT_PUBLISH`. Down reverses. Verified up+down on a scratch DB (Q-22). |
| Domain | `internal/domain/execution.go` | `WorkflowRun`/`StepRun`/`ExecutionLog`, run statuses mirroring the CHECK (`canceled`, one `l`). Nullable columns are pointers. `domain.NodeTypeEventPublish`. |
| SSRF boundary | `internal/platform/safehttp/ssrf.go` + tests | **B-1**: denylist after DNS resolution (loopback, RFC 1918/4193, link-local incl. `169.254.169.254`, multicast, unspecified), pinned dial, per-redirect re-validation; allowlist is a dev-only override, ignored in production. Config default emptied. |
| Execution repo | `internal/execution/repository.go` | Atomic run claim (`UPDATE … WHERE status='pending' RETURNING`, zero rows = normal duplicate delivery), reaper (`ReclaimExpiredLeases`), lease heartbeat, tenant-scoped step/log writes. |
| Queue | `internal/platform/queue/asynq.go` | Message = tenant ID + run ID only (D-3). `Client` (EnqueueRun/Publish/QueueDepth), `Server` (drains `RunQueue`, ctx passes asynq cancellation). |
| Executors | `internal/execution/executor/*.go` | HTTP (interpolate + capped body via `io.LimitReader`), DELAY (`select`, no bare `time.Sleep`), CONDITION/TRANSFORM (engine evaluators), EVENT_PUBLISH (queue publisher). |
| Coordinator | `internal/execution/coordinator.go` | `Tick` rebuilds scope from persisted outputs; `CanTransition` gates every write; bounded errgroup; retry-with-backoff; liveness check marks stuck runs failed; heartbeat goroutine owned by a `WaitGroup`. |
| Metrics | `internal/platform/metrics/` | **B-2**: counters carry `tenant_id`, histograms do not — label sets pinned by `TestMetrics_LabelDiscipline` (Q-24). |
| Worker | `cmd/worker/main.go` | Full wiring; the `time.Sleep(100ms)` TODO is gone, replaced by `asynq.Server.Shutdown()` drain + reaper loop (reclaim → re-enqueue → queue-depth gauge). |
| Use case | `internal/execution/usecase.go` | `CreateRun` (enqueue-then-persist so a queue failure writes nothing), `CancelRun`. |

## Deviations from the plan (all deliberate, each stronger or necessary)

1. **Step claim is atomic `UPDATE … SET status='running' … WHERE id IN (SELECT … FOR UPDATE SKIP LOCKED) RETURNING`, not the literal `SELECT FOR UPDATE SKIP LOCKED`.** The literal SELECT-only form cannot prove "no step is claimed twice" (Q-19): with claim and mark-running as separate statements two workers can claim the same row. The atomic form enforces the `ready→running` transition in the DB and is required by AGENTS.md rule 3. Proven by Q-19: 30 iterations × 4 workers × 9 steps, every step claimed exactly once under `-race`.
2. **`B-1` was applied as planned**: `ALLOWED_HTTP_HOSTS` now defaults empty and is ignored in production-like envs. The compose dev override still lists loopback for local development.
3. **`B-2` deviation needs the user's acknowledgement** (per the plan): histograms (`step_duration`, `run_age`) carry no `tenant_id`, contradicting the backlog wording. Documented in the metric help strings and pinned by test.
4. `EVENT_PUBLISH` publishes onto the same asynq queue as an event task; the Phase 7 listener/outbox is out of scope.

## Verification

- `make ci` (fmt → vet → build → `test -race -count=1`): **exit 0**, no FAIL.
- `FLOWFORGE_INTEGRATION=1 make test-integration`: **exit 0**, no FAIL.
- Concurrency acceptance criteria, all under `-race`:
  - **Q-18** concurrent run claim: 16 goroutines × 50 iterations, exactly one winner each.
  - **Q-19** concurrent step claim: 4 workers × 30 iterations, no double-claim, no starvation.
  - **Q-20** every run status / node type / log level constant accepted by its CHECK.
  - **Q-21** reaper reclaims an expired lease and resets the run to pending.
  - **Q-22** 000001+000002+000003 apply cleanly from scratch; 000003 down reverses.
  - **Q-23** cancel mid-flight stops downstream dispatch; run lands in `canceled`.
  - **Queue E2E**: enqueue → asynq delivers → coordinator executes → `succeeded`, 2/2 steps, through real Redis.
  - Worker binary smoke-tested: starts, connects, drains on SIGTERM (`All workers have finished`), exits 0.
- Engine purity untouched (`TestEngine_ImportPurity` PASS); `internal/engine/` has no Phase 5 changes.
- Boundary checklist: every repo query `tenant_id`-scoped; no `internal/auth` import in `internal/execution`; all goroutines owned by errgroup/WaitGroup/asynq; executors honour `ctx` (DELAY via `select`); SSRF post-resolution + per-redirect.

## Notes for Phase 6

- `CreateRun`/`CancelRun` are the seams the HTTP handlers call; `GetRun`/`ListStepRuns`/`Append` exist for the run-details endpoints.
- A run is at-least-once: a step recorded `running` at reclaim time is retried, and `attempt_count` makes it visible. Exactly-once needs executor-side idempotency keys (Phase 6, per `api-3.md`).
- The reaper interval (15s) and `RUN_LEASE_DURATION` (60s) are config-driven; lease expiry is the crash-recovery latency.
