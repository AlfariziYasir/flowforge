# Implementation Plan — Phase 8 Review Remediation

Closes the 3 findings in [phase_8_review_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_8_review_findings.md) — 2 medium (AD-1, AD-2), 1 low (AD-3).

**AD-1 and AD-2 share a root cause theme** (D-8's metrics never fully reached the API process) but have distinct fixes — AD-1 is a wiring omission at a call site, AD-2 is a missing struct field. AD-3 is independent of both. Fix in any order.

---

## Phase AR — Wire D-8's Metrics All the Way (AD-1, AD-2)

### Step 1 — tests first, they must be RED

#### [MODIFY] [handler_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler_test.go)

- ★ **`TestHandler_StreamEvents_RecordsSSEConnectionsMetric`** — construct `NewExecutionHandlerWithStream` with a real `metrics.New(prometheus.NewRegistry())` instance (not `nil`), connect via `StreamEvents`, and assert `SSEConnections.WithLabelValues(tenantID.String())` reads `1` while connected and `0` after the context cancels and the handler returns. Use `testutil.ToFloat64` (`github.com/prometheus/client_golang/prometheus/testutil`) to read the gauge's current value directly — this is the pattern that would have caught AD-1, since it exercises the metric through the same handler construction path production uses, not just the `nil`-safety branch the existing test already covers.

#### [MODIFY] [usecase_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/usecase_test.go)

- ★ **`TestExecutionUseCase_PublishEvent_RecordsMetric`** — construct the usecase with a real `*metrics.Metrics` (once `ExecutionConfig` gains the field, see Step 2) and assert `EventsPublished.WithLabelValues(tenantID.String(), domain.EventRunCreated)` increments after `CreateRun`. Red today: `ExecutionConfig` has no field to pass a `*metrics.Metrics` into in the first place — this test can't even compile until Step 2 lands, which is itself proof of the gap.

### Step 2 — the fix

#### [MODIFY] [dto.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/dto.go)

Add the missing field, matching `CoordinatorConfig`'s existing `Metrics *metrics.Metrics` shape exactly:

```go
type ExecutionConfig struct {
	AIMaxRetries     int
	AIRequestTimeout time.Duration
	Events  eventstream.Publisher
	// Metrics may be nil; when set, usecase-level events increment EventsPublished.
	Metrics *metrics.Metrics
	Logger  *slog.Logger
}
```

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/usecase.go)

`publishEvent` records the metric the same way `Coordinator.publishEvent` already does — before the nil-check on `Events`, matching D-8's stated rule ("the metric measures 'an event was generated,' not 'delivery succeeded'"):

```go
func (uc *executionUseCase) publishEvent(ctx context.Context, ev domain.Event) {
	if uc.cfg.Metrics != nil {
		uc.cfg.Metrics.EventsPublished.WithLabelValues(ev.TenantID.String(), ev.Type).Inc()
	}
	if uc.cfg.Events == nil {
		return
	}
	if err := uc.cfg.Events.Publish(ctx, ev); err != nil {
		uc.cfg.Logger.Warn("eventstream: publish failed",
			slog.String("type", ev.Type),
			slog.String("tenantID", ev.TenantID.String()),
			slog.Any("error", err))
	}
}
```

#### [MODIFY] [cmd/api/main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)

Construct a `*metrics.Metrics` for the API process — `cmd/worker/main.go`'s existing `reg := prometheus.NewRegistry(); m := metrics.New(reg)` is the pattern to copy — and thread it through both places that were missing it:

```go
reg := prometheus.NewRegistry()
m := metrics.New(reg)
...
execUC := execution.NewExecutionUseCase(
	wfUC, execRepo, execRepo, execRepo, verRepo, queueClient, auditRepo, uow, aiProvider, execRepo,
	execution.ExecutionConfig{
		AIMaxRetries:     cfg.AIMaxRetries,
		AIRequestTimeout: cfg.AIRequestTimeout,
		Events:           eventPub,
		Metrics:          m,
		Logger:           log,
	},
)
executionHandler = execution.NewExecutionHandlerWithStream(execUC, clientMgr, m)
```

`reg` itself doesn't need to be exposed anywhere yet — no `/metrics` HTTP endpoint exists in either process (see the findings doc's "Notes, not findings" — genuinely out of this fix's scope, predates Phase 8). Constructing `m` is still correct and necessary: it's what makes the in-process values non-zero and observable by anything that reads the `*Metrics` struct directly (tests, or a future `/metrics` endpoint that doesn't need to touch this wiring again when it lands).

#### [MODIFY] [metrics.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/metrics/metrics.go)

Update the package doc comment — it currently says "Package metrics exposes FlowForge **worker** telemetry," which is the exact mental model that produced AD-1:

```go
// Package metrics exposes FlowForge telemetry — collected by both cmd/worker
// (run/step execution) and cmd/api (SSE connections, usecase-level events).
```

### Verification — Phase AR

```
go test ./internal/execution/ -run 'TestHandler_StreamEvents_RecordsSSEConnectionsMetric|TestExecutionUseCase_PublishEvent_RecordsMetric' -race -count=1 -v
# both green

# mutation: revert cmd/api/main.go's NewExecutionHandlerWithStream call back to a literal `nil`
# TestHandler_StreamEvents_RecordsSSEConnectionsMetric... doesn't directly exercise main.go, so this
# mutation targets the handler test's own construction instead — pass nil there and confirm the test
# correctly distinguishes "guarded, doesn't panic" from "actually records":
#   NewExecutionHandlerWithStream(stub, fakeMgr, nil) → SSEConnections read via testutil.ToFloat64
#   on a *different* registry (nil metrics can't report through the real one) — MUST fail to compile
#   or MUST read 0 forever, proving the test's assertion is meaningful, not tautological.

grep -n "Metrics:" cmd/api/main.go
# present in the ExecutionConfig construction — was previously absent entirely

grep -n "NewExecutionHandlerWithStream(execUC, clientMgr, nil)" cmd/api/main.go
# MUST return empty — the literal nil is gone
```

---

## Phase AS — Concurrent `ClientManager` Test (AD-3)

#### [MODIFY] [eventstream_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/eventstream/eventstream_test.go)

- ★ **`TestClientManager_ConcurrentRegisterUnregister`** (integration, real Redis, `FLOWFORGE_INTEGRATION=1`) — 50+ goroutines concurrently calling `Register`/`unregister` (including a double-`unregister` per goroutine, proving `sync.Once` holds) against the same tenant, run under `-race`. Assert no panic, no race, and that after all goroutines finish, the tenant's Redis subscription is fully torn down (the manager's internal `subs` map has no entry for the tenant — this needs either an exported introspection method or asserting indirectly: register one more client after the storm and confirm it successfully receives a published event, proving the subscription goroutine wasn't left in a broken half-torn-down state).

```go
func TestClientManager_ConcurrentRegisterUnregister(t *testing.T) {
	rdb := getTestRedis(t)
	defer rdb.Close()
	mgr := eventstream.NewClientManager(rdb)
	defer func() { _ = mgr.Close() }()

	tenantID := uuid.New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, unreg := mgr.Register(tenantID)
			unreg()
			unreg() // double-unregister must be safe (sync.Once)
		}()
	}
	wg.Wait()

	// Prove the subscription tore down cleanly and can be re-established:
	// register fresh and confirm a published event is actually received.
	_, events, unreg := mgr.Register(tenantID)
	defer unreg()
	time.Sleep(100 * time.Millisecond)

	pub := eventstream.NewRedisPublisher(rdb)
	require.NoError(t, pub.Publish(context.Background(), domain.Event{Type: domain.EventHeartbeat, TenantID: tenantID}))

	select {
	case <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("post-storm registration never received an event — subscription left in a broken state")
	}
}
```

### Verification — Phase AS

```
FLOWFORGE_INTEGRATION=1 go test ./internal/platform/eventstream/... -race -count=5 -run TestClientManager_ConcurrentRegisterUnregister -v
# green, 5 repetitions, no race — matches the manual probe this review already ran once
```

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| AR — Metrics wiring | AD-1, AD-2 | 2 ★ tests green; `Metrics:` present in `cmd/api/main.go`'s `ExecutionConfig` and handler construction |
| AS — Concurrent ClientManager test | AD-3 | ★ test green ×5 under `-race` |

After each phase: `make ci` **and** `FLOWFORGE_INTEGRATION=1 make test-integration`, both green, before appending to `.agents/memory/action_history.md`.

Guarantees that must not be lost:

```
FLOWFORGE_INTEGRATION=1 go test ./internal/platform/eventstream/ -run TestRedisPublisherAndClientManager_Integration -race -count=1
# cross-tenant isolation and fan-out — the phase's headline behavior

go test ./internal/auth/... -race -count=1
# the AuthMiddleware refactor's zero-regression guarantee — untouched by this remediation, must stay green
```

---

## Notes for Phase 9

- Whenever a `/metrics` HTTP endpoint is finally added to either process (carried forward, not this phase's job either), it needs nothing from AD-1/AD-2's fix beyond what's already there — `m := metrics.New(reg)` already registers everything with `reg`; exposing `reg` via `promhttp.HandlerFor(reg, ...)` on a route is the only remaining step, and it can reuse the exact `reg`/`m` this fix constructs in `cmd/api/main.go` rather than building a second one.
- D-4's placeholder-free status and the rest of Phase 8's carried-forward items (rate limiting, CORS, HTTP security headers, `idempotency_keys`) are unchanged.
