# Phase AN–AP (Phase 7 Remediation) — Verification Review

Review of the execution of [phase_7_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_7_review_remediation_plan.md).

**Verdict: 🟡 CONDITIONAL — 0 critical, 0 high, 2 medium, 1 low** (AC-1, AC-2, AC-3)

> [!IMPORTANT]
> **All three production fixes (AB-1, AB-2, AB-3) are genuinely correct.** Every finding in this round is about the *tests* written to guard them, or a CI-hygiene gate, not about the fixes themselves. This is good news for correctness and bad news for confidence: two of the three new regression tests do not actually protect what they claim to.

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` reports **2 files** · `go test ./... -race -count=1` **FAILS** (one race). `make ci` fails at its first step (`fmt-check`) — it never reaches the build or test stages.

```
$ make ci
gofmt reported unformatted files:
internal/execution/repository_test.go
internal/platform/eventbus/eventbus_test.go
make: *** [Makefile:17: fmt-check] Error 1
```

---

## Scorecard

| Finding | Result |
|---|---|
| **AB-1** — NATS subscriber dies on one bad message | ✅ **Production fix is correct** — `Subscribe`'s per-message loop now `continue`s instead of `return err`ing. Confirmed by mutation. ⚠️ Its own regression test has a data race — see **AC-2**. |
| **AB-2** — wait tokens permanently exhaust their correlation key | ✅ **Production fix is correct** — partial unique index (`WHERE consumed_at IS NULL AND handled_at IS NULL`) + `handled_at` column, threaded through `ConsumeTokenIfUnconsumed`/`MarkTokenHandled`/`FindExpiredTokens`/`CountActiveTokens` consistently. Confirmed live: a correlation key can now be reused after consumption. ⚠️ The sweeper-starvation half of this fix has a test that doesn't test it — see **AC-3**. |
| **AB-3** — NATS healthcheck/startup race | ✅ **Fully fixed and confirmed live on a cold start.** `flowforge-nats` reports `healthy` within seconds of `docker compose up`; `worker` now has `depends_on: nats: condition: service_healthy`; `nats.Connect` has `RetryOnFailedConnect`/`MaxReconnects(-1)`/`ReconnectWait`. |
| Housekeeping — consolidate `action-log.md` | ✅ Done. The separate file is gone; its content is folded into `action_history.md`. |

### What was verified live, not just read

- Brought the full stack up cold (`docker compose up -d postgres redis nats migrate seed`) specifically to exercise AB-3's actual failure scenario — a fresh startup race. `flowforge-nats` went `healthy` in ~20s with no manual intervention.
- Probed the NATS monitoring endpoint directly (`docker exec flowforge-nats wget -qO- http://localhost:8222/healthz` → `{"status":"ok"}`) — the port AB-3 was supposed to enable.
- Ran `FLOWFORGE_INTEGRATION=1 go test ./internal/execution/... ./internal/platform/eventbus/...` — every test passes except the one with the race (AC-2 below); `TestNATS_EventResolvesWaitToken`, `TestCreateToken_ReusesCorrelationKeyAfterConsumption`, `TestSweeper_AdvancesPastAlreadyHandledExpiredTokens`, and Phase 5's `TestRunClaim_ExactlyOneWinner` (the carried-forward guarantee) all pass.
- Directly queried Postgres after the tests ran and found genuine duplicate `(tenant_id, correlation_key)` rows in `step_wait_tokens` — concrete proof the partial index does what it's supposed to (a plain `UNIQUE` constraint could never have permitted this).

---

## 🟡 Medium

### AC-1: `TestSubscribe_OneBadMessageDoesNotStopSubsequentProcessing` has a genuine data race — `go test ./... -race` fails

[eventbus_test.go:181-221](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/eventbus/eventbus_test.go#L181-L221)

The test's `fakeHandler` struct has plain, unsynchronized fields (`lastKey`, `received`, `resolved`, `orphans`). `Subscribe` runs in a background goroutine and writes `handler.lastKey` inside `fakeHandler.HandleEvent` (called via `handleNATSMessage`); the test's own goroutine polls `handler.lastKey` in a tight loop with no lock, channel, or atomic:

```go
go func() {
    _ = Subscribe(subCtx, nc, subject, "durable-"+suffix, secretGetter, handler, nil)
}()
...
for time.Now().Before(deadline) {
    if handler.lastKey == "GOOD-1" {   // <- unsynchronized read, races the goroutine's write
```

Confirmed live:

```
WARNING: DATA RACE
Write at 0x00c0001cf6c8 by goroutine 14: ...fakeHandler.HandleEvent()...
Previous read at 0x00c0001cf6c8 by goroutine 9: ...TestSubscribe_OneBadMessageDoesNotStopSubsequentProcessing()...
```

**The underlying production fix is not at fault.** I confirmed this independently: reverting `nats.go`'s `continue` back to `return err` and running the test *without* `-race` correctly fails it (`"subscriber stopped processing after bad message"`) — the fix logic itself is sound. The race is entirely in the test's own polling mechanism.

This matters because `go test ./... -race -count=1` is this project's own unconditional CI gate (`Makefile`'s `test` target), used as the baseline check before every review in this session. A flaky, race-flagged test in the suite means `make ci` cannot pass reliably — and worse, `-race`'s detection is probabilistic goroutine-scheduling-dependent, so this could pass locally on one run and fail on the next, exactly the kind of test that erodes trust in "the suite is green."

### AC-2: `TestSweeper_AdvancesPastAlreadyHandledExpiredTokens` doesn't actually exercise the sweeper's fix — it provides zero regression protection

[repository_test.go:561-622](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/repository_test.go#L561-L622) · [sweeper.go:26-29](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/sweeper.go#L26-L29)

The remediation plan asked for a test proving the sweeper "advances past" a backlog of already-handled expired tokens across **multiple ticks** — the actual starvation scenario AB-2 diagnosed. The test instead seeds its 105 "backlog" tokens as already-handled by calling the repository method directly, bypassing the sweeper entirely:

```go
for i := 0; i < 105; i++ {
    ...
    require.NoError(t, repo.CreateToken(ctx, token))
    // Mark token handled (via sweep or MarkTokenHandled)
    require.NoError(t, repo.MarkTokenHandled(ctx, tenantID, token.ID))   // <- direct call, not through SweepExpiredWaitTokens
}
...
swept, err := execution.SweepExpiredWaitTokens(ctx, repo, repo, fakeEnq, nil)  // called ONCE, not twice
```

Since the 105 backlog rows are marked handled *before* the sweeper ever runs, `FindExpiredTokens` never returns them regardless of whether `SweepExpiredWaitTokens`'s own `MarkTokenHandled` call exists. The single active token is found and processed either way — the test cannot distinguish "the sweeper marks tokens handled" from "the sweeper doesn't, but something else already did."

**Confirmed by mutation, exactly as the remediation plan specified as the gate**: removing `sweeper.go`'s `_ = tokens.MarkTokenHandled(ctx, t.TenantID, t.ID)` line entirely —

```
go test ./internal/execution/ -run TestSweeper_AdvancesPastAlreadyHandledExpiredTokens
--- PASS  ← should have FAILED, per the remediation plan's own stated gate
```

**The production code is correct regardless** — `sweeper.go` does call `MarkTokenHandled` unconditionally, before the `ErrStepNotFound` branch, which is actually a cleaner placement than the remediation plan's literal suggestion (it covers both the success and not-found paths for free). But if a future change to the sweeper silently dropped or broke that call, this test would not catch it.

---

## 🟢 Low

### AC-3: `make ci` fails immediately — two test files aren't `gofmt`-clean

[repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/repository_test.go) · [eventbus_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/eventbus/eventbus_test.go)

Both new test files end with a trailing blank line `gofmt` wants removed:

```
$ gofmt -l ./cmd ./internal
internal/execution/repository_test.go
internal/platform/eventbus/eventbus_test.go
```

Trivial to fix (`gofmt -w`), but concretely means the tree, right now, cannot pass `make ci` — the project's own single verification command fails at the very first step (`fmt-check`), never reaching `vet`, `build`, or `test`. Whatever verification produced this round's "DONE" status in `action_history.md`, it was not a clean `make ci` run against the final state of the tree.

---

## Notes, not findings

- **`Makefile`'s `up` target still doesn't start `nats`** (`docker compose up -d postgres redis migrate seed` — no `nats`). This predates this remediation round (it wasn't in AB-3's scope, which only touched `docker-compose.yml` and `cmd/worker/main.go`), but it's the same theme AB-3 addressed and was missed. Anyone following the documented `make up` → `make test-integration` workflow gets every NATS-dependent integration test silently skipping (`t.Skipf("nats not reachable...")`) rather than running for real, unless they separately remember to `docker compose up -d nats`.

---

## Remediation

See [phase_7_remediation_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_7_remediation_verification_plan.md) — Phase AQ. All three findings are independent and fixable in any order; none touch `nats.go`, `sweeper.go`, `repository.go`, `docker-compose.yml`, or any migration — every change is to test code or a formatting pass.
