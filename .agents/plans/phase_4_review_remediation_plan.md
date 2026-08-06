# Implementation Plan — Phase 4 Review Remediation

Closes the 4 findings in [phase_4_review_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_4_review_findings.md) — 1 high (W-1), 1 medium (W-2), 2 low (W-3, W-4).

**W-1 blocks Phase 5.** The worker is built directly on `CalculateReadyNodes`; a run stuck this way offers no symptom to diagnose — no error, no timeout, it simply stops.

---

## Phase AE — Fix Readiness Classification (W-1, W-2, W-3)

### Step 1 — tests first, they must be RED

#### [MODIFY] [readiness_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness_test.go)

- ★ **`TestReadiness_ConditionalJoin`** — the heart of this fix. An if/else-then-join graph, stepped through **twice**:

  ```go
  // A(CONDITION) --true--> B --default--> D
  //              --false--> C --default--> D
  // Step 1: A succeeds(true)        → ready=[b], skipped=[c]
  // Step 2: B succeeds, C skipped   → ready=[d]   ← RED today (ready=[])
  ```

  The **second step** is the point. The existing `TestReadiness_ConditionBranching` stops at step one, which is exactly why this bug got through.

- **`TestReadiness_ConditionalJoin_FalseBranch`** — the mirror image: A succeeds(`false`), C succeeds, B skipped → `ready=[d]`. Proves the fix is symmetric rather than a patch on one side.

- **`TestReadiness_AllBranchesSkipped`** — when every inbound edge is dead, the node still becomes `skipped`. Guards against the W-1 fix making everything ready.

- ★ **`TestReadiness_ConditionMissingResult`** *(W-2)* — a `CONDITION` succeeding without `output["result"]` must **error**, not silently produce `skipped=[b c]`.

### Step 2 — the fix

#### [MODIFY] [readiness.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness.go)

**W-1** — replace the two mutually inconsistent flags (`allPredecessorsSucceeded` + `allInboundEdgesDead`) with an explicit per-edge classification. Three categories, mutually exclusive:

| Category | When | Meaning |
|---|---|---|
| **live** | predecessor `succeeded` **and** its branch was taken | this path is alive and finished |
| **dead** | predecessor `skipped`, **or** `succeeded` but branch not taken | this path will never arrive |
| **blocking** | predecessor `pending`/`ready`/`running`/`waiting`/`failed` | this path is still in motion or stuck |

The decision then becomes simple and gap-free:

```go
switch {
case blocking > 0:
    // stay pending — some path has not finished
case live == 0:
    // every path is dead → skipped (transitively)
default:
    // at least one live path, nothing blocking → ready
}
```

Compare with today: a `skipped` predecessor sets `allPredecessorsSucceeded = false` **without** marking its edge dead, so the node falls between the two checks. Under the three-category model `skipped` lands in **dead** — and a node with one live path becomes ready immediately.

> [!NOTE]
> This also removes `allPredecessorsSucceeded` entirely. The name was misleading from the start: the right question is not *"did every predecessor succeed?"* but *"is any path still in motion?"*.

**W-2** — `conditionResult` must now distinguish "absent" from "false":

```go
// A CONDITION in status succeeded MUST produce a boolean output["result"].
// Its absence is an executor bug, not a valid state — return an error rather than
// silently killing both branches.
var ErrConditionResultMissing = errors.New("condition node succeeded without a boolean result")
```

Return `fmt.Errorf("%w: node %q", ErrConditionResultMissing, key)` from `CalculateReadyNodes` when a `CONDITION` predecessor is `succeeded` but `result` is missing or not a `bool`.

**W-3** — update the doc comment to match the new implementation:

```
//  1. A node is ready when at least one inbound path is live (predecessor succeeded
//     and its branch was taken) AND no inbound path is still in motion.
//     This is an OR-join over dead branches, an AND-join over readiness.
```

### Test Strategy — Phase AE

Both ★ cases **must be red before the fix and green after.** If `TestReadiness_ConditionalJoin` passes from the start, the test is wrong — the bug has not gone away on its own.

Verify by mutation after the fix:

| Mutation | Expected |
|---|---|
| Move `skipped` back into the **blocking** category | `TestReadiness_ConditionalJoin` fails |
| Make `conditionResult` return `(false, true)` when `result` is missing | `TestReadiness_ConditionMissingResult` fails |

`TestReadiness_FailedPredecessor` and `TestReadiness_WaitingPredecessor` **must stay green** — both fall into *blocking* and their behaviour must not change.

---

## Phase AF — Read the Snapshot Back (W-4)

Optional for Phase 4, but **worth doing before Phase 6** exposes `GetVersion` to clients.

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go)

`GetVersion` currently always uses `LoadGraph`, including for `published` versions. Restore the behaviour Phase 3's finding G-9 asked for:

```go
if ver.Status == domain.VersionStatusDraft {
    // draft: assemble from nodes/edges — its snapshot is still '{}'
    g = domain.FromPersisted(nodes, edges)
} else {
    // published: the snapshot is the truth; that is what immutability is for
    if err := json.Unmarshal(ver.GraphSnapshot, &g); err != nil { ... }
    g.Normalize()   // B-6: older snapshots carry no "branch" key
}
```

This also makes `Normalize()` genuinely useful, and makes the checksum P-2 guards protect a value that is **actually read**.

### Test Strategy — Phase AF
- `GetVersion` on a published version returns the snapshot's contents, **not** `LoadGraph`'s. Prove it by deliberately making the two differ in the mock.
- `GetVersion` on a draft still uses `LoadGraph` (regression guard for Phase 3's T-31/P-31).
- A snapshot with no `branch` key normalises to `default` — extends P-2b onto the real path.

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| **AE — Readiness** | W-1, W-2, W-3 | 2 ★ cases red → green; both mutations fail; failed/waiting tests stay green |
| AF — Snapshot | W-4 | `GetVersion` on published reads the snapshot |

After each phase: `make ci` (`fmt-check` → `vet` → `build` → `test -race -count=1`) must exit zero **before** the execution entry is appended to `.agents/memory/action_history.md`.

Guarantees that must not be lost:

```
go test ./internal/engine/ -run TestEngine_ImportPurity -count=1
# temporarily inject a forbidden import → MUST fail (P-22b)

grep -n "allPredecessorsSucceeded" internal/engine/readiness.go
# must be empty after AE — the flag is removed, not patched around
```

---

## Notes for Phase 5

The W-1 fix changes a contract the worker will rely on, so it is worth recording now:

- `CalculateReadyNodes` can now return an **error** for a structurally valid graph (W-2). The worker must treat that as a failed run, not panic.
- A node appearing in neither `ready` nor `skipped` means it is **still waiting on another path** — a legitimate state, not a deadlock. After AE, the only way a run can stall is a `failed`/`waiting` predecessor, and both have their own handling.
- That property is worth guarding in Phase 5 with a liveness check: if a run has no `running`/`ready`/`waiting` step while `pending` steps remain, the run is stuck and should be marked failed rather than left alone.
