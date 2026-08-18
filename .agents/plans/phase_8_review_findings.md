# Phase 8 (Real-Time Monitoring, v2) — Review Findings

Review of the execution of [phase_8_realtime_monitoring.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_8_realtime_monitoring.md).

**Verdict: 🟡 CONDITIONAL — 0 high, 2 medium, 1 low** (AD-1, AD-2, AD-3)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` empty · `go test ./... -race -count=1` clean · `FLOWFORGE_INTEGRATION=1 go test ./... -race -count=1` clean · `make ci` exit 0.

> [!NOTE]
> **The core feature works and is well-built.** SSE streaming, the auth refactor, tenant isolation, and the best-effort publish discipline are all correct and thoroughly tested — several places even better than the plan's own sample code. Every finding here is about D-8's metrics being incompletely wired, or a missing test the plan explicitly asked for. Nothing here breaks streaming, auth, or execution.

---

## What Was Done Right — and it is substantial

- **The `AuthMiddleware` refactor is exactly what D-1 demanded, verified against the actual diff, not just the final state.** `git diff` confirms the header-only path is a pure extraction — same branching, same error messages, byte-for-byte — with the query-param fallback strictly gated to fire only when the header is absent. All revocation checks (`blacklist.IsRevoked`, `sessionStore.IsUserRevoked`, `sessionStore.GetSession`) live in the shared body untouched, so the query-param path gets identical security checking. The full pre-existing `internal/auth` test suite (28+ seconds, dozens of tests) passes unchanged, and a dedicated `TestAuthenticateWithTokenSource` proves a blacklisted token is rejected identically via the query param — the exact test the plan called the "proof, not a read of the diff."
- **`domain.Event`'s JSON tags were corrected from the plan's own (broken) sample.** The plan's §2.1 struct tagged `Type`, `ID`, and `TenantID` as `json:"-"` — which would have silently dropped the event type from every message the moment it crossed the Redis Pub/Sub wire (marshal on the worker side, unmarshal on the API side), making every SSE event arrive with an empty type. The executor caught this and gave them real tags (`type`, `id`, `tenantId`). Confirmed live via the Redis integration test that events survive the round-trip with type intact.
- **All 8 run events and 4+1 step events are wired**, not just the 7 the plan's own table enumerated: `coordinator.go` carries 9 (including `step.started`/`workflow.run.started`, correctly placed exactly where the plan's §2.4 note said they'd have to be — earlier than the log-writer's 7 sites), `usecase.go` carries `workflow.run.created`/`.queued`/`.retryRequested`/`.cancelRequested`/`.cancelled`, and `analysis.go` carries `workflow.analysis.completed`. The plan's own carried-forward open item ("`step.waiting` has no §11.6 event name — flagged, not silently resolved") was resolved by adding `EventStepWaiting` rather than left silent.
- **Best-effort discipline (D-4) is correctly implemented and tested at both layers** — `Coordinator.publishEvent` and `executionUseCase.publishEvent` both swallow publish errors into a `Logger.Warn`, never propagate. `TestCoordinator_RealTimeEvents_PublishedPerTransition/publish_error_does_not_fail_execution` proves it.
- **`ClientManager`'s reference-counting and cross-tenant isolation are correct**, verified two ways: read through carefully (the `unregister` critical section deletes the client entry and closes its channel under the same mutex `fanOut` reads through, so there's no window for a send-on-closed-channel panic), and confirmed live with a hand-written concurrent-registration probe under `-race` (50 goroutines, double-unregister included) — clean, no race, `sync.Once` correctly guards against double-cleanup.
- **`X-Accel-Buffering: no`** was added to the SSE response headers beyond the plan's sample — a real-world reverse-proxy gotcha (nginx buffers by default, breaking SSE) the plan didn't mention.

---

## 🟡 Medium

### AD-1: `SSEConnections` is wired but never populated — `cmd/api/main.go` passes `nil`

[cmd/api/main.go:321](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L321) · [handler.go:33-42](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go#L33-L42)

```go
executionHandler = execution.NewExecutionHandlerWithStream(execUC, clientMgr, nil)
```

The third argument is `*metrics.Metrics`. `cmd/api/main.go` has **zero other references to the `metrics` package anywhere** — it never calls `metrics.New(reg)`, unlike `cmd/worker/main.go` which does. `StreamEvents`'s `if h.metrics != nil { ... }` guard is correctly written and correctly prevents a nil-pointer panic, but it also means the guard's true branch can never execute in production: **no matter how many SSE clients connect, `SSEConnections` reads zero, permanently.**

This is squarely new-to-this-phase: `SSEConnections` only has meaning in the API process (that's where `GET /api/v1/events` lives), and the process where it was supposed to be recorded never got a metrics instance to record into. `internal/platform/metrics`'s own package doc comment — "Package metrics exposes FlowForge **worker** telemetry" — reveals the worker-only mental model that led to this gap; the doc comment itself needs updating alongside the fix.

### AD-2: `EventsPublished` silently undercounts — 6 of the ~14 event types are never recorded

[dto.go:94-102](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/dto.go#L94-L102) · [usecase.go:357-365](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/usecase.go#L357-L365)

`Coordinator.publishEvent` increments `EventsPublished` correctly (confirmed: `Metrics` is threaded into `CoordinatorConfig` in `cmd/worker/main.go`). But `ExecutionConfig` — the config struct backing `executionUseCase.publishEvent` — **has no `Metrics` field at all**:

```go
type ExecutionConfig struct {
	AIMaxRetries     int
	AIRequestTimeout time.Duration
	Events eventstream.Publisher
	Logger *slog.Logger
}
```

So `workflow.run.created`, `workflow.run.queued`, `workflow.run.cancelRequested`, `workflow.run.cancelled`, `workflow.run.retryRequested`, and `workflow.analysis.completed` — six of the event types this phase added — publish to Redis and stream to browsers correctly (that part works), but never touch `EventsPublished` at all. The counter silently reports only the coordinator's ~half of real event volume, which would mislead anyone using it to answer "how many runs were created this week" (always reads the coordinator-only subset, i.e. zero for `run.created` specifically, since that event type is *only* ever published from `usecase.go`).

---

## 🟢 Low

### AD-3: `ClientManager`'s concurrent register/unregister isn't tested under `-race`, as the plan's boundary checklist required

[eventstream_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/eventstream/eventstream_test.go)

The plan's §3 boundary checklist item: *"`ClientManager`'s Redis subscription is reference-counted (D-5), proven under `-race` with concurrent register/unregister."* Only two tests exist — a noop smoke test and a sequential (not concurrent) Redis integration test proving fan-out and isolation. Neither exercises concurrent `Register`/`unregister` calls.

**The underlying code is not at fault** — I wrote and ran a concurrent probe (50 goroutines registering and double-unregistering against the same tenant, `-race`, 5 repetitions) and it passed clean every time, matching what a careful read of the locking already suggested (the critical sections in `fanOut` and `unregister` are fully serialized by the same mutex, so there's no window for a send-on-closed-channel panic). This is test debt, not a live defect — but it's the specific regression protection the plan asked for and it's the kind of concurrency correctness that's easy to break silently in a later refactor without a test catching it.

---

## Notes, not findings

- **No process in this codebase exposes a `/metrics` HTTP scrape endpoint** — not `cmd/api`, not `cmd/worker`, not before this phase either. This predates Phase 8 by several phases (`internal/platform/metrics` has existed since Phase 5) and isn't this phase's job to fix; noted because it's the reason AD-1/AD-2's practical blast radius is smaller today than the metric names alone suggest — but it doesn't make the in-process wiring gap not a bug relative to what D-8 asked this phase to deliver.
- **`domain.Event.Data map[string]any`** was added by the executor beyond the plan's original struct but is never populated at any call site. Harmless (unused, not broken), just unused extensibility — not worth a finding.

---

## Remediation

See [phase_8_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_8_review_remediation_plan.md). AD-1 and AD-2 share a root theme (D-8 incompletely wired) but have distinct fixes; AD-3 is independent of both.
