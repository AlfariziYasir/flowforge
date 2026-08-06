# Execution Record — Phase 5 Review Remediation (Y-1 … Y-4)

Plan: [phase_5_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_5_review_remediation_plan.md). **Status: DONE — both suites green, all three mutations verified to fail.**

## Phase AH — Lease Ownership and Loop Safety (Y-1, Y-2, Y-3)

### Y-1 — the lease is now a lease (fencing token)
- `ExtendLease(ctx, tenantID, runID, workerID, lease)` gained the `claimed_by = workerID` predicate; zero rows → new sentinel `ErrLeaseLost`.
- `Tick` re-validates ownership every round: `*run.ClaimedBy != WorkerID → ErrLeaseLost` before touching anything.
- The heartbeat now distinguishes `ErrLeaseLost` (cancel the shared work context, abandon the run) from transient failures (log and keep trying). `HandleRun` and the heartbeat share one cancellable `workCtx`; a lease loss stops the loop promptly.
- **Additional bug the test caught**: on context cancellation mid-execution, `runStepWithRetry` previously wrote `retrying` — mutating a run we no longer own. It now returns the cancellation error without writing, leaving the step `running` for the reaper to reset.

### Y-3 — the reaper recovers orphans atomically
- `ReclaimExpiredLeases` is now one statement: a CTE resets the run to `pending`/cleared lease AND resets its `running`/`ready` steps to `retrying` with `attempt_count + 1` (the recommended poison-pill guard). The two writes are inseparable.
- `ClaimReadySteps` now claims `('ready', 'retrying')` so a reclaimed orphan is re-dispatched by the next worker, and `RetryPolicy.MaxAttempts` eventually gives up on a step that crashes its worker every time.

### Y-2 — the loop has a brake
- `Tick`'s `more` now means *progress* (this worker dispatched work) only. When nothing is dispatchable and the run is not finished, `HandleRun` idles `TickIdleDelay` (new config, default 1s) instead of spinning — the durable fix that covers any future `waiting`-style parked state (Phase 7), independent of Y-3.

## Phase AI — SSRF Coverage (Y-4)
- `validateIP` now also denies `100.64.0.0/10` (CGNAT), `0.0.0.0/8`, `192.0.0.0/24`, `198.18.0.0/15`, and `64:ff9b::/96` (NAT64).
- `resolveAndPin` aligned with `ValidateHostname`: **fail closed** — one non-public address rejects the whole host, so the two checks can never disagree. Updated the doc comment to match.

## TDD & Mutation Verification

| Gate | Result |
|---|---|
| 4 ★ tests RED before fix | ✅ `TestExtendLease_RejectsNonOwner` & `TestReclaim_ResetsRunningSteps` failed pre-fix (non-owner extended; steps untouched); `TestHandleRun_StopsWhenLeaseLost` & `TestHandleRun_DoesNotSpinOnOrphanedStep` failed (wrote retrying on cancel; spun) |
| 4 ★ tests GREEN after fix | ✅ |
| Mutation: drop `claimed_by` predicate | ✅ `TestExtendLease_RejectsNonOwner` FAILS |
| Mutation: remove step reset from CTE | ✅ `TestReclaim_ResetsRunningSteps` FAILS |
| Mutation: remove idle brake | ✅ `TestHandleRun_DoesNotSpinOnOrphanedStep` FAILS |
| `TestRunClaim_ExactlyOneWinner` (Q-18) | ✅ stays green (16 workers × 50 iters) |
| `TestStepClaim_NoDoubleClaimNoStarvation` (Q-19) | ✅ stays green (4 workers × 30 iters) |
| Y-4: 5 new addresses blocked; `8.8.8.8` + `2001:4860:4860::8888` pass | ✅ |
| Y-4: `resolveAndPin` agrees with `ValidateHostname` (multi-address) | ✅ |

## Full Verification
- `make ci` exit 0; `FLOWFORGE_INTEGRATION=1 make test-integration` exit 0.
- `TestEngine_ImportPurity` PASS; engine untouched.
- `grep claimed_by internal/execution/repository.go` — predicate present in `ExtendLease` (line 211), not just `ClaimRun`.

## Notes for Phase 6
- `ExtendLease` signature changed (added `workerID`); `internal/execution` is the only caller.
- At-least-once steps remain a documented property; the fence narrows the window but a worker can still be mid-HTTP when its lease expires. Exactly-once needs Phase 6 idempotency keys.
- Reclaim-driven attempts now increment `attempt_count`; Phase 6's retry UI should surface reclaim-driven attempts distinctly from failure-driven ones.
