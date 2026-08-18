# Implementation Plan — Phase AQ: Fix the Test Gaps (AC-1, AC-2, AC-3)

Closes the 3 findings in [phase_7_remediation_verification.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_7_remediation_verification.md) — 2 medium (AC-1, AC-2), 1 low (AC-3).

**None of AB-1/AB-2/AB-3's production fixes are in question.** All three were independently confirmed correct in that review (by mutation, and by live reproduction against a cold Docker stack and real Postgres data). Every item here is about the *tests* that guard those fixes, or a formatting gate — no `nats.go`, `sweeper.go`, `repository.go`, `docker-compose.yml`, or migration file needs to change. AC-1, AC-2, and AC-3 are independent; fix in any order.

---

## AC-1 — Synchronize `fakeHandler` (data race under `-race`)

#### [MODIFY] [eventbus_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/eventbus/eventbus_test.go)

`fakeHandler`'s fields are written from the background `Subscribe` goroutine (via `HandleEvent`/`RecordOrphanEvent`) and read unsynchronized from the test's polling loop in `TestSubscribe_OneBadMessageDoesNotStopSubsequentProcessing`. Add a mutex and a locked accessor:

```go
type fakeHandler struct {
    mu       sync.Mutex
    resolved bool
    received string
    lastKey  string
    orphans  int
}

func (f *fakeHandler) HandleEvent(ctx context.Context, tenantID uuid.UUID, correlationKey string, payload []byte) (bool, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.lastKey = correlationKey
    f.received = string(payload)
    return f.resolved, nil
}

func (f *fakeHandler) RecordOrphanEvent(ctx context.Context, tenantID uuid.UUID, correlationKey string, payload []byte, reason string) error {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.orphans++
    return nil
}

func (f *fakeHandler) LastKey() string {
    f.mu.Lock()
    defer f.mu.Unlock()
    return f.lastKey
}
```

Update every read site across the file (there are a handful of other tests using `fakeHandler` besides the new AC-1 one — check `TestGRPC_ServerAndClientRoundTrip` and any others that read `.received`/`.orphans`/`.resolved` directly) to go through locked accessors, or at minimum confirm those other read sites are single-goroutine (call `HandleEvent` synchronously in the same goroutine that asserts) and don't need one. Only `TestSubscribe_...`'s polling loop is confirmed racy — don't add accessors the other tests don't need, but don't leave a second race unaddressed either.

In `TestSubscribe_OneBadMessageDoesNotStopSubsequentProcessing`, change the poll:

```go
for time.Now().Before(deadline) {
    if handler.LastKey() == "GOOD-1" {
        assert.Equal(t, "GOOD-1", handler.LastKey())
        return
    }
    time.Sleep(100 * time.Millisecond)
}
```

### Verification

```
go test ./internal/platform/eventbus/... -race -count=1 -run TestSubscribe_OneBadMessageDoesNotStopSubsequentProcessing -v
# must pass under -race — this is the actual gate; it already passes without -race today
```

---

## AC-2 — Make the sweeper test exercise the sweeper

#### [MODIFY] [repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/repository_test.go)

`TestSweeper_AdvancesPastAlreadyHandledExpiredTokens` currently pre-marks its 105 backlog tokens handled via a direct `repo.MarkTokenHandled` call and runs the sweeper once — neither exercises the sweeper's own responsibility for marking tokens handled, nor the two-tick shape that reproduces real starvation. Rewrite:

```go
func TestSweeper_AdvancesPastAlreadyHandledExpiredTokens(t *testing.T) {
    pool := getTestPool(t)
    defer pool.Close()
    ctx := context.Background()
    repo := execution.NewExecutionRepository(pool)

    tenantID, wfID, verID, nodes := seedRunFixtures(t, ctx, pool, 1)

    // Seed 105 expired, genuinely UNHANDLED tokens — no direct MarkTokenHandled
    // call. Their ExpiresAt is earlier than the active token's, so ORDER BY
    // expires_at puts them first in FindExpiredTokens.
    for i := 0; i < 105; i++ {
        run := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfID, WorkflowVersionID: verID}
        _, err := repo.CreateRun(ctx, run)
        require.NoError(t, err)
        require.NoError(t, repo.CreateStepRuns(ctx, tenantID, run.ID, nodes))
        steps, err := repo.ListStepRuns(ctx, tenantID, run.ID)
        require.NoError(t, err)
        _, err = pool.Exec(ctx, `UPDATE step_runs SET status='waiting' WHERE id=$1`, steps[0].ID)
        require.NoError(t, err)
        require.NoError(t, repo.UpdateRunStatus(ctx, tenantID, run.ID, domain.RunStatusWaiting, false))

        token := &domain.StepWaitToken{
            ID: uuid.New(), TenantID: tenantID, WorkflowRunID: run.ID, StepRunID: steps[0].ID,
            CorrelationKey: uuid.New().String(), ExpiresAt: time.Now().Add(-2 * time.Hour),
        }
        require.NoError(t, repo.CreateToken(ctx, token))
    }

    // 1 more recently-expired token whose step is genuinely still waiting.
    activeRun := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfID, WorkflowVersionID: verID}
    _, err := repo.CreateRun(ctx, activeRun)
    require.NoError(t, err)
    require.NoError(t, repo.CreateStepRuns(ctx, tenantID, activeRun.ID, nodes))
    steps, err := repo.ListStepRuns(ctx, tenantID, activeRun.ID)
    require.NoError(t, err)
    _, err = pool.Exec(ctx, `UPDATE step_runs SET status='waiting' WHERE id=$1`, steps[0].ID)
    require.NoError(t, err)
    require.NoError(t, repo.UpdateRunStatus(ctx, tenantID, activeRun.ID, domain.RunStatusWaiting, false))

    activeToken := &domain.StepWaitToken{
        ID: uuid.New(), TenantID: tenantID, WorkflowRunID: activeRun.ID, StepRunID: steps[0].ID,
        CorrelationKey: "ACTIVE-SWEEP-KEY", ExpiresAt: time.Now().Add(-1 * time.Minute),
    }
    require.NoError(t, repo.CreateToken(ctx, activeToken))

    fakeEnq := &fakeEnqueuer{}

    // First sweep: FindExpiredTokens(limit=100) returns the 100 oldest of the
    // 106 expired tokens — all backlog, none of them the active one (it's
    // newest). The sweeper must mark all 100 handled itself; without the fix,
    // nothing does, and they resurface forever.
    swept1, err := execution.SweepExpiredWaitTokens(ctx, repo, repo, fakeEnq, nil)
    require.NoError(t, err)
    require.Equal(t, 100, swept1, "first sweep processes exactly the LIMIT")

    // Second sweep: only reaches the remaining 5 backlog tokens plus the active
    // one if the first sweep's 100 no longer resurface. This is the actual
    // regression AB-2 diagnosed and the line this test must catch losing.
    swept2, err := execution.SweepExpiredWaitTokens(ctx, repo, repo, fakeEnq, nil)
    require.NoError(t, err)
    assert.Equal(t, 6, swept2, "second sweep must advance past the first 100 to reach the remaining backlog and the active token")

    gotRun, err := repo.GetRun(ctx, tenantID, activeRun.ID)
    require.NoError(t, err)
    assert.Equal(t, domain.RunStatusPending, gotRun.Status, "the active run must have been woken")
}
```

The key change from the current test: **no direct `MarkTokenHandled` call in the setup loop**, and **the sweeper runs twice with an assertion on each call's count**, not once. `swept1 == 100` pins `FindExpiredTokens`'s `LIMIT 100` firing on the first call; `swept2 == 6` is the actual regression assertion — without AC-2's fix (i.e., under the exact mutation the review applied), the first 100 backlog tokens would still have `handled_at IS NULL` after the first sweep, so the second sweep's `FindExpiredTokens` would return the *same* 100 rows again (not the remaining 5 + the active one), and `swept2` would be `0` for the active token's purposes — `gotRun.Status` would still be `waiting`, failing the final assertion.

### Verification

```
FLOWFORGE_INTEGRATION=1 go test ./internal/execution/ -run TestSweeper_AdvancesPastAlreadyHandledExpiredTokens -v

# mutation — re-run the exact gate the original remediation plan specified:
# remove sweeper.go's `_ = tokens.MarkTokenHandled(ctx, t.TenantID, t.ID)` line
FLOWFORGE_INTEGRATION=1 go test ./internal/execution/ -run TestSweeper_AdvancesPastAlreadyHandledExpiredTokens -v
# MUST fail this time — confirm it does before restoring sweeper.go
```

---

## AC-3 — `gofmt` the two files

```
gofmt -w internal/execution/repository_test.go internal/platform/eventbus/eventbus_test.go
```

No manual review needed — this only removes the trailing blank line at EOF `gofmt -l` already flagged. Re-run `gofmt -l ./cmd ./internal` afterward to confirm it's empty; if AC-1/AC-2's edits above introduce any new formatting drift, this same command cleans it up regardless of order.

---

## Execution Order & Verification

All three are independent; there's no benefit to a specific order. Do AC-3 last regardless, since it's a mechanical pass that should run after AC-1/AC-2's edits are in place, not before.

```
gofmt -w internal/execution/repository_test.go internal/platform/eventbus/eventbus_test.go   # after AC-1/AC-2 edits

make ci
# fmt-check → vet → build → test (-race -count=1), must exit 0 — the actual bar this round missed

FLOWFORGE_INTEGRATION=1 go test ./internal/execution/... ./internal/platform/eventbus/... -race -count=1 -v
# every test green, no race warnings anywhere in this run
```

Guarantees that must not be lost:

```
FLOWFORGE_INTEGRATION=1 go test ./internal/execution/ -run TestRunClaim_ExactlyOneWinner -race -count=1
# Phase 5's headline guarantee

FLOWFORGE_INTEGRATION=1 go test ./internal/execution/ -run TestNATS_EventResolvesWaitToken -race -count=1
# Phase 7's own happy-path proof

docker inspect flowforge-nats --format '{{.State.Health.Status}}'
# must still print "healthy" — nothing here should touch AB-3's fix, but confirm the stack is still up
```

After `make ci` exits 0, append the execution entry to `.agents/memory/action_history.md` (the established single history file — not a new one).

---

## Notes for Phase 8

Carried forward unchanged from the verification review:

- `Makefile`'s `up` target still doesn't start `nats` (`docker compose up -d postgres redis migrate seed`). Not in this plan's scope (it's a pre-existing gap, not something AC-1/AC-2/AC-3 touch), but worth a one-line fix (`docker compose up -d postgres redis nats migrate seed`) whenever Phase 8 next touches the Makefile, so `make up && make test-integration` actually exercises NATS instead of silently skipping it.
- The gRPC signing-over-re-marshaled-bytes fragility (from the original Phase 7 review, still not scored as a finding) remains unaddressed.
- D-4's placeholder wire contract status is unchanged.
