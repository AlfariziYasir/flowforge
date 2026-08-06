# Implementation Plan — Phase 5 Review Remediation

Closes the 4 findings in [phase_5_review_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_5_review_findings.md) — 2 high (Y-1, Y-2), 2 medium (Y-3, Y-4).

**Y-1, Y-2 and Y-3 are one connected story:** the lease is never checked, the reaper leaves steps orphaned, and the loop has no brake. Fix them together — patching any one alone leaves the failure reachable by another route.

---

## Phase AH — Lease Ownership and Loop Safety (Y-1, Y-2, Y-3)

### Step 1 — tests first, they must be RED

#### [MODIFY] [repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/repository_test.go) *(integration — real Postgres)*

- ★ **`TestExtendLease_RejectsNonOwner`** — worker A claims; worker B calls `ExtendLease` with its own ID; B's call must report **not-owner** and leave `lease_expires_at` untouched. Red today: `ExtendLease` has no owner predicate and succeeds for anyone.

- ★ **`TestReclaim_ResetsRunningSteps`** — seed a run with a step in `running` and an expired lease; after `ReclaimExpiredLeases`, that step must be back to `pending` (or `retrying` with `attempt_count` incremented, per §6 of the original plan). Red today: `step_runs` is never touched.

#### [MODIFY] [coordinator_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/coordinator_test.go) *(unit — mocked repos)*

- ★ **`TestHandleRun_StopsWhenLeaseLost`** — mock `ExtendLease` to return `ErrLeaseLost` on its second call; assert `HandleRun` returns promptly and dispatches no further steps.

- ★ **`TestHandleRun_DoesNotSpinOnOrphanedStep`** — a run whose only non-terminal step is `running`, with nothing claimable. Assert `Tick` is called a **bounded** number of times (e.g. ≤ 3) rather than unbounded. Red today: it loops forever.

  > Implementation note: count `GetRun` calls on the mock and fail past a threshold, with a `context.WithTimeout` on `HandleRun` so a red test terminates instead of hanging the suite.

### Step 2 — the fix

#### [MODIFY] [repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/repository.go)

- **Y-1** — `ExtendLease` takes the worker identity and asserts ownership:

  ```go
  var ErrLeaseLost = errors.New("lease no longer held by this worker")

  func (r *executionRepository) ExtendLease(ctx context.Context,
      tenantID, runID uuid.UUID, workerID string, lease time.Duration) error {
      // ... Where(sq.Eq{
      //         "tenant_id": tenantID, "id": runID,
      //         "status": domain.RunStatusRunning,
      //         "claimed_by": workerID,          // ← the fencing predicate
      //      })
      if tag.RowsAffected() == 0 {
          return ErrLeaseLost
      }
      return nil
  }
  ```

  Zero rows now means something specific: the run finished, or another worker owns it. Either way this worker must stop.

- **Y-3** — `ReclaimExpiredLeases` resets orphaned steps in the **same transaction** as the run reset. A run returned to `pending` with a `running` step is not recoverable, so the two writes must not be separable:

  ```sql
  WITH reclaimed AS (
      UPDATE workflow_runs
         SET status = 'pending', claimed_by = NULL, lease_expires_at = NULL, updated_at = NOW()
       WHERE status = 'running' AND lease_expires_at < NOW()
      RETURNING id, tenant_id
  )
  UPDATE step_runs s
     SET status = 'pending', updated_at = NOW()
    FROM reclaimed r
   WHERE s.workflow_run_id = r.id AND s.tenant_id = r.tenant_id
     AND s.status IN ('running', 'ready')
  ```

  `ready` is included deliberately: a step marked ready but never claimed is equally orphaned.

  > [!NOTE]
  > The original plan §6 said a reclaimed `running` step is "retried, and `attempt_count` makes the retry visible". Incrementing `attempt_count` here would make the retry countable and let `RetryPolicy.MaxAttempts` eventually give up on a step that crashes its worker every time — a poison-pill guard. Decide explicitly: reset to `pending` (simple, but a poison step retries forever) or `retrying` with `attempt_count + 1` (matches §6). **Recommend the latter.**

#### [MODIFY] [coordinator.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/coordinator.go)

- **Y-1** — the heartbeat must stop the run when the lease is lost, not just log:

  ```go
  case <-ticker.C:
      if err := c.runs.ExtendLease(ctx, tenantID, runID, c.cfg.WorkerID, c.cfg.Lease); err != nil {
          if errors.Is(err, ErrLeaseLost) {
              c.cfg.Logger.Warn("lease lost, abandoning run", ...)
              cancelRun()          // cancel the ctx HandleRun's loop watches
              return
          }
          c.cfg.Logger.Warn("lease heartbeat failed", ...)   // transient: keep trying
      }
  ```

  This needs the heartbeat to hold a cancel func for the *work* context, not just its own. Restructure so `HandleRun` derives one cancellable context that both the loop and the heartbeat share.

- **Y-2** — `HandleRun`'s loop needs a brake. Two changes, both required:

  1. **Bound the "waiting on someone else" case.** `finishRun` returning `more = true` with nothing claimable means this worker has no work. Rather than re-ticking immediately, return and let the queue redeliver — or sleep a configurable `TickIdleDelay` (default 1s) before the next iteration.
  2. **Distinguish "I dispatched work" from "someone else might be working".** `didWork` already carries the first; `finishRun`'s `anyActive` conflates the second with it. Return them separately so the loop only spins tight when it is actually making progress.

  > [!IMPORTANT]
  > Fixing Y-3 removes today's known orphan source, but the loop must not depend on that. Any future state where `anyActive` is true and nothing is claimable — a `waiting` step in Phase 7, for one — reintroduces the spin. **The brake is the durable fix; Y-3 is the specific leak.**

### Test Strategy — Phase AH

All four ★ cases red before, green after. Then verify by mutation:

| Mutation | Expected |
|---|---|
| Drop the `claimed_by` predicate from `ExtendLease` | `TestExtendLease_RejectsNonOwner` fails |
| Remove the `step_runs` update from the reclaim CTE | `TestReclaim_ResetsRunningSteps` fails |
| Remove the idle brake from `HandleRun` | `TestHandleRun_DoesNotSpinOnOrphanedStep` fails |

`TestRunClaim_ExactlyOneWinner` and `TestStepClaim_NoDoubleClaimNoStarvation` **must stay green** — they are the phase's acceptance criteria and this change touches the same rows.

---

## Phase AI — SSRF Coverage (Y-4)

#### [MODIFY] [ssrf.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/safehttp/ssrf.go)

- **Y-4** — extend `validateIP` beyond what `netip`'s helpers cover. `IsPrivate()` is RFC 1918 + RFC 4193 only; the rest need explicit prefixes:

  ```go
  // Ranges netip's helpers do not classify, but which are non-public in practice.
  // 100.64.0.0/10 is the one that matters most: AWS EKS pod networking, GCP, and
  // several managed-Kubernetes providers place internal endpoints there.
  var extraDenied = []netip.Prefix{
      netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT, RFC 6598
      netip.MustParsePrefix("0.0.0.0/8"),       // "this network", RFC 1122
      netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
      netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking, RFC 2544
      netip.MustParsePrefix("64:ff9b::/96"),    // NAT64 — can embed any IPv4
  }
  ```

  For `64:ff9b::/96`, rejecting the whole prefix is simpler and safer than extracting the embedded IPv4 and re-validating it.

- **Note** — align `resolveAndPin` with `ValidateHostname`, or align the doc comment with the code. The struct comment promises *"safe only if every address it resolves to is public"*; `resolveAndPin` implements "safe if any address is public". Both are safe in practice, but a reader will trust the comment. **Recommend making `resolveAndPin` fail closed too** — consistency is worth more than the marginal availability of dual-stack hosts with one bad address.

### Test Strategy — Phase AI
- Extend the `Q-13` table with the five newly-denied addresses plus `8.8.8.8` and `2001:4860:4860::8888` as the must-pass controls.
- One case proving `resolveAndPin` and `ValidateHostname` agree on a multi-address host (whichever policy is chosen).

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| **AH — Lease & loop** | Y-1, Y-2, Y-3 | 4 ★ cases red → green; 3 mutations fail; the two claim tests stay green |
| AI — SSRF coverage | Y-4 | Denylist table extended; `8.8.8.8` still passes |

After each phase: `make ci` **and** `FLOWFORGE_INTEGRATION=1 make test-integration`, both green, **before** the execution entry is appended to `.agents/memory/action_history.md`.

Guarantees that must not be lost:

```
FLOWFORGE_INTEGRATION=1 go test ./internal/execution/ -run TestRunClaim_ExactlyOneWinner -count=1
# 16 workers × 50 iterations, exactly one winner — the phase's headline criterion

go test ./internal/engine/ -run TestEngine_ImportPurity -count=1
# inject a forbidden import → MUST fail

grep -n "claimed_by" internal/execution/repository.go
# ExtendLease must carry the predicate, not just ClaimRun
```

---

## Notes for Phase 6

- `ExtendLease` gains a `workerID` parameter — a signature change, but `internal/execution` is the only caller.
- **At-least-once step execution remains a documented property.** Y-1's fix narrows the window (a worker that loses its lease now stops) but does not close it: a worker can be mid-HTTP-POST when its lease expires. Exactly-once still needs executor-side idempotency keys, which `api-3.md` places in Phase 6.
- If Y-3 adopts `attempt_count + 1` on reclaim, Phase 6's retry UI should surface reclaim-driven attempts distinctly from failure-driven ones — otherwise a run that survived a worker crash looks like a run that kept failing.
